package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type CustomSMSQueueResult struct {
	Status    string `json:"status"`
	LogID     int64  `json:"log_id,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
}

type CustomSMSService struct {
	Client *http.Client
	Now    func() time.Time
}

var CustomSMS = &CustomSMSService{
	Client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	Now:    time.Now,
}

var customSMSCredentialPattern = regexp.MustCompile(`(?i)/ck/[^\s<>"'，。；！？]+|sk-[A-Za-z0-9_-]+|[A-Za-z0-9]{48,}`)

func RedactCustomSMSText(text string) string {
	return customSMSCredentialPattern.ReplaceAllString(text, "[redacted]")
}

func NormalizeCustomSMSPhone(phone string) string {
	var digits strings.Builder
	for _, c := range phone {
		if c >= '0' && c <= '9' {
			digits.WriteRune(c)
		}
	}
	result := digits.String()
	if len(result) == 13 && strings.HasPrefix(result, "86") {
		result = result[2:]
	}
	return result
}

func customSMSDB() *gorm.DB {
	// Queries and failed writes may contain contact details or upstream messages.
	return model.DB.Session(&gorm.Session{Logger: model.DB.Logger.LogMode(logger.Silent)})
}

type customSMSMessage struct{ phone, content, version, snapshot string }

func (s *CustomSMSService) prepare(token *model.Token) (customSMSMessage, string) {
	var result customSMSMessage
	if token.CustomSourceTokenID == nil || *token.CustomSourceTokenID <= 0 {
		return result, "source_missing"
	}
	var template model.CustomSMSTemplate
	if err := customSMSDB().First(&template, "source_token_id = ?", *token.CustomSourceTokenID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return result, "template_missing"
		}
		return result, "storage_error"
	}
	body := strings.TrimSpace(template.Content)
	result.version = fmt.Sprintf("%x", sha256.Sum256([]byte(template.Content)))
	if body == "" || len(body) > 16384 || !utf8.ValidString(body) || strings.ContainsAny(strings.ReplaceAll(body, "{custom_share_url}", ""), "{}\x00") {
		return result, "template_invalid"
	}
	if token.Status != common.TokenStatusEnabled && token.Status != common.TokenStatusExhausted || token.ExpiredTime != -1 && token.ExpiredTime <= s.Now().Unix() {
		return result, "share_unavailable"
	}
	user, err := model.GetUserById(token.UserId, false)
	if err != nil || user.Status != common.UserStatusEnabled {
		return result, "share_unavailable"
	}
	link := CustomTokenShareURL(token.ActiveCustomShareCode())
	parsed, err := url.Parse(link)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return result, "share_unavailable"
	}
	result.phone = NormalizeCustomSMSPhone(token.CustomPhone)
	if len(result.phone) < 7 || len(result.phone) > 15 {
		return result, "phone_invalid"
	}
	result.content = strings.ReplaceAll(body, "{custom_share_url}", link)
	result.snapshot = RedactCustomSMSText(body)
	result.snapshot = strings.ReplaceAll(result.snapshot, token.Key, "[redacted]")
	if _, code := customSMSURL(); code != "" {
		return result, code
	}
	return result, ""
}

func customSMSURL() (string, string) {
	raw := strings.TrimSpace(os.Getenv("CUSTOM_SMS_TONGQIUBAO_URL"))
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || os.Getenv("CUSTOM_SMS_TONGQIUBAO_ACCOUNT") == "" || os.Getenv("CUSTOM_SMS_TONGQIUBAO_PASSWORD") == "" || os.Getenv("CUSTOM_SMS_TONGQIUBAO_EXTNO") == "" {
		return "", "configuration_invalid"
	}
	return raw, ""
}

func (s *CustomSMSService) QueueAutomatic(token *model.Token, actorID int) CustomSMSQueueResult {
	message, code := s.prepare(token)
	sourceID := 0
	if token.CustomSourceTokenID != nil {
		sourceID = *token.CustomSourceTokenID
	}
	entry := model.CustomSMSSendLog{TokenID: token.Id, UserID: token.UserId, SourceTokenID: sourceID,
		Account: os.Getenv("CUSTOM_SMS_TONGQIUBAO_ACCOUNT"), Phone: token.CustomPhone, RequestID: uuid.NewString(), IdempotencyKey: "automatic", ActorID: actorID,
		SubmitStatus: "queued", CreatedAt: s.Now().Unix(), TemplateVersion: message.version, Content: message.snapshot, ErrorCode: code}
	if code != "" {
		entry.SubmitStatus = "failed"
	}
	if err := customSMSDB().Create(&entry).Error; err != nil {
		common.SysError("custom SMS record creation failed")
		return CustomSMSQueueResult{Status: "not_recorded", ErrorCode: "storage_error"}
	}
	return CustomSMSQueueResult{Status: entry.SubmitStatus, LogID: entry.ID, ErrorCode: code}
}

func (s *CustomSMSService) ManualBlockReason(token *model.Token) string {
	if _, code := s.prepare(token); code != "" {
		return code
	}
	_, reason, err := model.CustomSMSManualEligibility(customSMSDB(), token.Id, s.Now())
	if err != nil {
		return "storage_error"
	}
	return reason
}

func (s *CustomSMSService) QueueManual(token *model.Token, actorID int, key string) (CustomSMSQueueResult, string) {
	var existing model.CustomSMSSendLog
	if err := customSMSDB().Where("token_id = ? AND idempotency_key = ?", token.Id, key).First(&existing).Error; err == nil {
		return CustomSMSQueueResult{Status: existing.SubmitStatus, LogID: existing.ID, ErrorCode: existing.ErrorCode}, ""
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return CustomSMSQueueResult{}, "storage_error"
	}
	message, code := s.prepare(token)
	if code != "" {
		return CustomSMSQueueResult{}, code
	}
	entry := model.CustomSMSSendLog{TokenID: token.Id, UserID: token.UserId, SourceTokenID: *token.CustomSourceTokenID, Account: os.Getenv("CUSTOM_SMS_TONGQIUBAO_ACCOUNT"), Phone: token.CustomPhone, RequestID: uuid.NewString(), IdempotencyKey: key, Manual: true, ActorID: actorID, TemplateVersion: message.version, Content: message.snapshot, SubmitStatus: "queued", CreatedAt: s.Now().Unix()}
	if err := model.CreateCustomSMSManualAttempt(&entry, s.Now()); err != nil {
		code := "storage_error"
		if err.Error() == "rate_limited" || err.Error() == "send_in_progress" {
			code = err.Error()
		}
		return CustomSMSQueueResult{}, code
	}
	return CustomSMSQueueResult{Status: entry.SubmitStatus, LogID: entry.ID, ErrorCode: entry.ErrorCode}, ""
}

type CustomSMSProviderEvent struct {
	Flag       *int   `json:"flag"`
	MID        string `json:"mid"`
	Account    string `json:"spid"`
	AccessCode string `json:"accessCode"`
	Mobile     string `json:"mobile"`
	Content    string `json:"content"`
	Stat       string `json:"stat"`
	Time       string `json:"time"`
	Label      string `json:"label"`
	Result     *int   `json:"result"`
}

type customSMSProviderResponse struct {
	Status *int                     `json:"status"`
	List   []CustomSMSProviderEvent `json:"list"`
}

func (s *CustomSMSService) tongqiubaoRequest(ctx context.Context, payload map[string]any) (customSMSProviderResponse, string) {
	var result customSMSProviderResponse
	endpoint, code := customSMSURL()
	if code != "" {
		return result, code
	}
	payload["account"] = os.Getenv("CUSTOM_SMS_TONGQIUBAO_ACCOUNT")
	payload["password"] = os.Getenv("CUSTOM_SMS_TONGQIUBAO_PASSWORD")
	body, err := common.Marshal(payload)
	if err != nil {
		return result, "configuration_invalid"
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return result, "configuration_invalid"
	}
	request.Header.Set("Content-Type", "application/json;charset=utf-8")
	// A submitted SMS is not replayable, including transport retries and redirects.
	request.GetBody = nil
	request.Close = true
	client := *s.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return result, "response_unknown"
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return result, "response_unknown"
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil || len(data) > 1<<20 || common.Unmarshal(data, &result) != nil || result.Status == nil {
		return customSMSProviderResponse{}, "response_unknown"
	}
	return result, ""
}

// ProcessPending advances a bounded batch once; it never resubmits a claimed attempt.
func (s *CustomSMSService) ProcessPending(ctx context.Context) error {
	now := s.Now().Unix()
	db := customSMSDB().WithContext(ctx)
	if err := db.Model(&model.CustomSMSSendLog{}).Where("submit_status = ? AND claimed_at <= ?", "sending", now-60).Updates(map[string]any{"submit_status": "unknown", "error_code": "process_interrupted"}).Error; err != nil {
		return err
	}
	if err := db.Model(&model.CustomSMSSendLog{}).Where("submit_status = ? AND delivery_status = ? AND submitted_at <= ?", "accepted", "pending", now-86400).Updates(map[string]any{"delivery_status": "unknown", "error_code": "report_timeout"}).Error; err != nil {
		return err
	}
	var pending []model.CustomSMSSendLog
	if err := db.Where("submit_status = ?", "queued").Order("id").Limit(20).Find(&pending).Error; err != nil {
		return err
	}
	for _, entry := range pending {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		claimed := db.Model(&model.CustomSMSSendLog{}).Where("id = ? AND submit_status = ?", entry.ID, "queued").Updates(map[string]any{"submit_status": "sending", "claimed_at": s.Now().Unix()})
		if claimed.Error != nil {
			return claimed.Error
		}
		if claimed.RowsAffected != 1 {
			continue
		}
		var token model.Token
		code := ""
		message := customSMSMessage{}
		if err := db.First(&token, entry.TokenID).Error; err != nil {
			code = "share_unavailable"
		} else {
			message, code = s.prepare(&token)
		}
		if code != "" {
			if err := db.Model(&entry).Updates(map[string]any{"submit_status": "failed", "error_code": code}).Error; err != nil {
				return err
			}
			continue
		}
		account := os.Getenv("CUSTOM_SMS_TONGQIUBAO_ACCOUNT")
		if err := db.Model(&entry).Updates(map[string]any{"account": account, "phone": message.phone, "template_version": message.version, "content": message.snapshot}).Error; err != nil {
			return err
		}
		response, failure := s.tongqiubaoRequest(ctx, map[string]any{"action": "p2p", "extno": os.Getenv("CUSTOM_SMS_TONGQIUBAO_EXTNO"), "label": entry.RequestID, "mobileContentKvp": map[string]string{message.phone: message.content}})
		updates := map[string]any{"submit_status": "unknown", "error_code": "response_unknown"}
		if failure == "configuration_invalid" {
			updates["submit_status"], updates["error_code"] = "failed", failure
		}
		if failure == "" {
			updates["provider_status"] = *response.Status
			if *response.Status != 0 {
				updates["submit_status"], updates["error_code"] = "failed", "provider_rejected"
			} else {
				for _, item := range response.List {
					if NormalizeCustomSMSPhone(item.Mobile) != message.phone || item.Result == nil {
						continue
					}
					updates["provider_result"] = *item.Result
					if *item.Result != 0 {
						updates["submit_status"], updates["error_code"] = "failed", "provider_rejected"
					} else if item.MID != "" && len(item.MID) <= 128 {
						updates["submit_status"], updates["delivery_status"], updates["message_id"], updates["submitted_at"], updates["error_code"] = "accepted", "pending", item.MID, s.Now().Unix(), ""
					}
					break
				}
			}
		}
		if err := model.CompleteCustomSMSAttempt(entry.ID, updates); err != nil {
			return err
		}
	}
	return nil
}

func (s *CustomSMSService) ReceiveEvents(input []CustomSMSProviderEvent, source string) error {
	if len(input) > 1000 {
		return errors.New("callback_invalid")
	}
	events := make([]model.CustomSMSEvent, 0, len(input))
	for _, item := range input {
		contact := model.Token{CustomPhone: item.Mobile}
		if item.Flag == nil || (*item.Flag != 0 && *item.Flag != 1) || item.Account == "" || item.Account != os.Getenv("CUSTOM_SMS_TONGQIUBAO_ACCOUNT") || item.MID == "" || len(item.MID) > 128 || len(item.Label) > 128 || len(item.AccessCode) > 32 || !strings.HasPrefix(item.AccessCode, os.Getenv("CUSTOM_SMS_TONGQIUBAO_EXTNO")) || contact.BeforeCreate(nil) != nil || len(item.Content) > 16384 || !utf8.ValidString(item.Content) || len(item.Stat) > 80 {
			return errors.New("callback_invalid")
		}
		if _, err := time.Parse("2006-01-02 15:04:05", item.Time); err != nil {
			return errors.New("callback_invalid")
		}
		item.Mobile = NormalizeCustomSMSPhone(item.Mobile)
		item.Result = nil
		if *item.Flag == 1 {
			if item.Stat == "" || strings.ContainsAny(item.Stat, "\x00\r\n") {
				return errors.New("callback_invalid")
			}
			item.Content = ""
		} else {
			item.Stat = ""
			item.Label = ""
		}
		canonical, err := common.Marshal(item)
		if err != nil {
			return errors.New("callback_invalid")
		}
		events = append(events, model.CustomSMSEvent{Fingerprint: fmt.Sprintf("%x", sha256.Sum256(canonical)), Flag: *item.Flag, Account: item.Account, MessageID: item.MID, Label: item.Label, AccessCode: item.AccessCode, Phone: item.Mobile, Content: RedactCustomSMSText(item.Content), Stat: item.Stat, Time: item.Time, Source: source, CreatedAt: s.Now().Unix()})
	}
	if err := model.PersistCustomSMSEvents(events); err != nil {
		return errors.New("storage_error")
	}
	return nil
}

func (s *CustomSMSService) PullUpstream(ctx context.Context) (int, string) {
	response, code := s.tongqiubaoRequest(ctx, map[string]any{"action": "mo", "size": 1000})
	if code != "" {
		return 0, code
	}
	if *response.Status != 0 {
		return 0, "provider_rejected"
	}
	for _, item := range response.List {
		if item.Flag == nil || *item.Flag != 0 {
			return 0, "callback_invalid"
		}
	}
	if err := s.ReceiveEvents(response.List, "pull"); err != nil {
		return 0, err.Error()
	}
	return len(response.List), ""
}

func (s *CustomSMSService) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if err := s.ProcessPending(ctx); err != nil && ctx.Err() == nil {
			common.SysError("custom SMS processing failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
