package controller

import (
	"errors"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/console_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

type customTokenDetails struct {
	ID             int      `json:"id"`
	Name           string   `json:"name"`
	Key            string   `json:"key"`
	Status         int      `json:"status"`
	ExpiredTime    int64    `json:"expired_time"`
	RemainQuota    int      `json:"remain_quota"`
	UsedQuota      int      `json:"used_quota"`
	UnlimitedQuota bool     `json:"unlimited_quota"`
	CustomPhone    string   `json:"custom_phone"`
	APIAddresses   []string `json:"api_addresses"`
	Models         []string `json:"models"`
}

// Both creation and anonymous sharing use the same disclosure allowlist.
func buildCustomTokenDetails(c *gin.Context, token *model.Token) (*customTokenDetails, error) {
	ctx := c.Copy()
	if err := middleware.SetupContextForToken(ctx, token); err != nil {
		return nil, err
	}
	// Resolve the current owner group, never a group supplied by a share visitor.
	group, err := getTokenRequestUserGroup(ctx)
	if err != nil {
		return nil, err
	}
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, group)
	models, _, err := availableTokenModels(ctx)
	if err != nil {
		return nil, err
	}
	addresses := []string{}
	if console_setting.GetConsoleSetting().ApiInfoEnabled {
		for _, item := range console_setting.GetApiInfo() {
			if address, ok := item["url"].(string); ok && strings.TrimSpace(address) != "" {
				addresses = append(addresses, strings.TrimSpace(address))
			}
		}
	}
	if len(addresses) == 0 && strings.TrimSpace(system_setting.ServerAddress) != "" {
		addresses = append(addresses, strings.TrimRight(strings.TrimSpace(system_setting.ServerAddress), "/"))
	}
	status := token.Status
	if token.ExpiredTime != -1 && token.ExpiredTime <= common.GetTimestamp() {
		status = common.TokenStatusExpired
	} else if status == common.TokenStatusEnabled && !token.UnlimitedQuota && token.RemainQuota <= 0 {
		status = common.TokenStatusExhausted
	}
	return &customTokenDetails{
		ID: token.Id, Name: token.Name, Key: token.GetFullKey(), Status: status,
		ExpiredTime: token.ExpiredTime, RemainQuota: token.RemainQuota, UsedQuota: token.UsedQuota,
		UnlimitedQuota: token.UnlimitedQuota, CustomPhone: token.CustomPhone,
		APIAddresses: addresses, Models: models,
	}, nil
}

// DeriveToken copies configuration from an owned disabled token; it never changes the source.
func DeriveToken(c *gin.Context) {
	var request struct {
		SourceTokenID int     `json:"source_token_id"`
		OrderNo       string  `json:"custom_order_no"`
		Phone         string  `json:"custom_phone"`
		Name          *string `json:"name"`
		ValidDays     *int64  `json:"valid_days"`
		Amount        *int64  `json:"amount"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiError(c, errors.New("Invalid derivation request"))
		return
	}
	if request.SourceTokenID <= 0 || strings.TrimSpace(request.OrderNo) == "" || strings.TrimSpace(request.Phone) == "" {
		common.ApiError(c, errors.New("source_token_id, custom_order_no and custom_phone are required"))
		return
	}
	token, err := model.GetTokenByIds(request.SourceTokenID, c.GetInt("id"))
	if err != nil || token.Status != common.TokenStatusDisabled {
		common.ApiError(c, errors.New("Source API key must belong to you and be disabled"))
		return
	}
	params := tokenAuditParams(c)
	params["source_token_id"] = token.Id
	now := common.GetTimestamp()
	token.Id, token.Status, token.UsedQuota = 0, common.TokenStatusEnabled, 0
	token.CreatedTime, token.AccessedTime = now, now
	order := model.CustomCaseSensitiveString(request.OrderNo)
	token.CustomOrderNo, token.CustomPhone, token.CustomShareCode = &order, request.Phone, nil
	if request.Name != nil {
		token.Name = *request.Name
	}
	if len(token.Name) > 50 {
		common.ApiError(c, errors.New("API key name must not exceed 50 bytes"))
		return
	}
	if request.ValidDays != nil {
		if *request.ValidDays < 0 || *request.ValidDays > (math.MaxInt64-now)/86400 {
			common.ApiError(c, errors.New("Invalid validity days"))
			return
		}
		token.ExpiredTime = -1
		if *request.ValidDays > 0 {
			token.ExpiredTime = now + *request.ValidDays*86400
		}
	}
	if request.Amount != nil {
		if *request.Amount < 0 || *request.Amount > 1_000_000_000 || common.QuotaPerUnit <= 0 || math.IsNaN(common.QuotaPerUnit) || math.IsInf(common.QuotaPerUnit, 0) {
			common.ApiError(c, errors.New("Invalid amount or quota conversion setting"))
			return
		}
		quota, err := common.WalletQuotaFromDecimalStrict(decimal.NewFromInt(*request.Amount).Mul(decimal.NewFromFloat(common.QuotaPerUnit)))
		if err != nil {
			common.ApiError(c, errors.New("Amount exceeds the quota limit"))
			return
		}
		token.RemainQuota, token.UnlimitedQuota = quota, false
	}
	if err := token.BeforeCreate(nil); err != nil {
		common.ApiError(c, err)
		return
	}
	count, err := model.CountUserTokens(token.UserId)
	if err != nil || int(count) >= operation_setting.GetMaxUserTokens() {
		common.ApiError(c, errors.New("Unable to create API key: token limit or storage error"))
		return
	}
	token.Key, err = common.GenerateKey()
	if err != nil {
		common.ApiError(c, errors.New("Unable to generate API key"))
		return
	}
	if err := token.RegenerateCustomShare(); err != nil {
		common.ApiError(c, errors.New("Unable to generate share code"))
		return
	}
	details, err := buildCustomTokenDetails(c, token)
	if err != nil {
		common.ApiError(c, errors.New("Unable to load API key details"))
		return
	}
	if err := token.Insert(); err != nil {
		if errors.Is(err, model.ErrCustomOrderExists) {
			c.JSON(http.StatusConflict, gin.H{"success": false, "message": err.Error()})
		} else {
			common.ApiError(c, errors.New("Unable to create API key"))
		}
		return
	}
	details.ID = token.Id
	params["id"], params["name"] = token.Id, token.Name
	common.SetContextKey(c, constant.ContextKeyTokenAuditSucceeded, true)
	common.ApiSuccess(c, struct {
		*customTokenDetails
		OrderNo string `json:"custom_order_no"`
		Code    string `json:"custom_share_code"`
		URL     string `json:"custom_share_url"`
	}{details, string(*token.CustomOrderNo), token.ActiveCustomShareCode(), customShareURL(token.ActiveCustomShareCode())})
}

func customShareURL(code string) string {
	if code == "" {
		return ""
	}
	// Do not trust Host/X-Forwarded-Host for credential-bearing links.
	base := strings.TrimSpace(os.Getenv("CUSTOM_TOKEN_SHARE_BASE_URL"))
	if base == "" {
		base = strings.TrimSpace(system_setting.ServerAddress)
	}
	base = strings.TrimRight(base, "/")
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		base = ""
	} else {
		parsed.RawQuery, parsed.Fragment = "", ""
		base = strings.TrimRight(parsed.String(), "/")
	}
	return base + "/ck/" + code
}

func GetCustomTokenShare(c *gin.Context) {
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Robots-Tag", "noindex, nofollow, noarchive")
	var request struct {
		Code string `json:"custom_share_code"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Share link is unavailable"})
		return
	}
	token, err := model.GetTokenByCustomShare(request.Code)
	if err == nil {
		var user *model.User
		user, err = model.GetUserById(token.UserId, false)
		if err == nil {
			common.SetContextKey(c, constant.ContextKeyUserGroup, user.Group)
		}
		if err == nil && (user.Status != common.UserStatusEnabled ||
			(token.Status != common.TokenStatusEnabled && token.Status != common.TokenStatusExhausted) ||
			(token.ExpiredTime != -1 && token.ExpiredTime <= common.GetTimestamp())) {
			err = errors.New("unavailable")
		}
	}
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Share link is unavailable"})
		return
	}
	// Share authorization always uses the database; only quota counters may use
	// the live relay cache (which may lead DB writes when batching is enabled).
	live, err := model.GetTokenByKey(token.Key, false)
	if err != nil {
		common.ApiError(c, errors.New("Unable to load API key details"))
		return
	}
	token.RemainQuota, token.UsedQuota = live.RemainQuota, live.UsedQuota
	details, err := buildCustomTokenDetails(c, token)
	if err != nil {
		common.ApiError(c, errors.New("Unable to load API key details"))
		return
	}
	// Anonymous responses expose only the last four digits; stored contact data
	// and the authenticated issuance response retain the original phone number.
	if details.CustomPhone != "" {
		var digits strings.Builder
		for _, char := range details.CustomPhone {
			if char >= '0' && char <= '9' {
				digits.WriteRune(char)
			}
		}
		phone := digits.String()
		details.CustomPhone = "****" + phone[max(0, len(phone)-4):]
	}
	common.ApiSuccess(c, details)
}

// ManageCustomTokenShare is owner-only, and can only manage derived-token shares.
func ManageCustomTokenShare(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiError(c, errors.New("Invalid API key"))
		return
	}
	token, err := model.GetTokenByIds(id, c.GetInt("id"))
	if err != nil || token.CustomShareCode == nil {
		common.ApiError(c, errors.New("Share link is unavailable"))
		return
	}
	params := tokenAuditParams(c)
	params["id"] = token.Id
	switch c.Request.Method {
	case http.MethodPut:
		if err := token.RegenerateCustomShare(); err != nil {
			common.ApiError(c, errors.New("Unable to generate share code"))
			return
		}
		if err := token.UpdateCustomShare(); err != nil {
			common.ApiError(c, errors.New("Unable to update sharing"))
			return
		}
	case http.MethodDelete:
		if code := token.ActiveCustomShareCode(); code != "" {
			*token.CustomShareCode = model.CustomCaseSensitiveString("-" + code)
		}
		if err := token.UpdateCustomShare(); err != nil {
			common.ApiError(c, errors.New("Unable to update sharing"))
			return
		}
	}
	common.SetContextKey(c, constant.ContextKeyTokenAuditSucceeded, true)
	code := token.ActiveCustomShareCode()
	common.ApiSuccess(c, gin.H{"custom_share_code": code, "custom_share_url": customShareURL(code)})
}
