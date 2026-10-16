package model

import (
	"errors"
	"slices"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// CustomSMSTemplate is maintained directly by operators, never through the API.
type CustomSMSTemplate struct {
	SourceTokenID int    `gorm:"primaryKey;autoIncrement:false"`
	Content       string `gorm:"type:text"`
}

func CustomSMSManualEligibility(db *gorm.DB, tokenID int, now time.Time) (int64, string, error) {
	var latest CustomSMSSendLog
	if err := db.Where("token_id = ?", tokenID).Order("id DESC").First(&latest).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, "", err
	}
	if latest.ID != 0 && (latest.SubmitStatus == "queued" || latest.SubmitStatus == "sending" || latest.DeliveryStatus == "delivered" || latest.SubmitStatus == "accepted" && latest.DeliveryStatus == "pending") {
		return latest.ID, "send_in_progress", nil
	}
	var previous CustomSMSSendLog
	if err := db.Where("token_id = ? AND manual = ?", tokenID, true).Order("id DESC").First(&previous).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, "", err
	}
	if previous.ID != 0 && now.Unix()-previous.CreatedAt < 60 {
		return latest.ID, "rate_limited", nil
	}
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	var count int64
	if err := db.Model(&CustomSMSSendLog{}).Where("token_id = ? AND manual = ? AND created_at >= ? AND created_at < ?", tokenID, true, start.Unix(), start.AddDate(0, 0, 1).Unix()).Count(&count).Error; err != nil {
		return 0, "", err
	}
	if count >= 5 {
		return latest.ID, "rate_limited", nil
	}
	return latest.ID, "", nil
}

func CreateCustomSMSManualAttempt(entry *CustomSMSSendLog, now time.Time) error {
	return DB.Session(&gorm.Session{Logger: DB.Logger.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
		var token Token
		if err := lockForUpdate(tx).First(&token, entry.TokenID).Error; err != nil {
			return err
		}
		var existing CustomSMSSendLog
		if err := tx.Where("token_id = ? AND idempotency_key = ?", entry.TokenID, entry.IdempotencyKey).First(&existing).Error; err == nil {
			*entry = existing
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		previousID, reason, err := CustomSMSManualEligibility(tx, entry.TokenID, now)
		if err != nil {
			return err
		}
		if reason != "" {
			return errors.New(reason)
		}
		entry.PreviousID = previousID
		return tx.Create(entry).Error
	})
}

type CustomSMSSendLog struct {
	ID              int64  `json:"id"`
	TokenID         int    `json:"token_id" gorm:"uniqueIndex:idx_custom_sms_request,priority:1;index"`
	UserID          int    `json:"user_id" gorm:"index"`
	SourceTokenID   int    `json:"source_token_id"`
	Account         string `json:"-" gorm:"size:128"`
	Phone           string `json:"-" gorm:"size:32"`
	RequestID       string `json:"-" gorm:"size:36;uniqueIndex"`
	IdempotencyKey  string `json:"-" gorm:"size:64;uniqueIndex:idx_custom_sms_request,priority:2"`
	Manual          bool   `json:"manual"`
	ActorID         int    `json:"actor_id"`
	PreviousID      int64  `json:"previous_id"`
	TemplateVersion string `json:"template_version" gorm:"size:64"`
	Content         string `json:"content" gorm:"type:text"`
	SubmitStatus    string `json:"submit_status" gorm:"size:16;index"`
	DeliveryStatus  string `json:"delivery_status" gorm:"size:16"`
	ErrorCode       string `json:"error_code" gorm:"size:80"`
	MessageID       string `json:"message_id" gorm:"size:128;index"`
	ProviderStatus  *int   `json:"provider_status,omitempty"`
	ProviderResult  *int   `json:"provider_result,omitempty"`
	ProviderStat    string `json:"provider_stat" gorm:"size:80"`
	CreatedAt       int64  `json:"created_at" gorm:"autoCreateTime:false"`
	ClaimedAt       int64  `json:"claimed_at"`
	SubmittedAt     int64  `json:"submitted_at"`
	ReportedAt      int64  `json:"reported_at"`
	ReportTime      string `json:"report_time" gorm:"size:19"`
}

func PersistCustomSMSEvents(events []CustomSMSEvent) error {
	return DB.Session(&gorm.Session{Logger: DB.Logger.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
		for i := range events {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&events[i]).Error; err != nil {
				return err
			}
			if events[i].Flag == 1 {
				if err := applyCustomSMSReport(tx, &events[i]); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func applyCustomSMSReport(tx *gorm.DB, event *CustomSMSEvent) error {
	query := tx.Where("account = ? AND phone = ?", event.Account, event.Phone)
	if event.Label != "" {
		query = query.Where("request_id = ? AND (message_id = ? OR message_id = '')", event.Label, event.MessageID)
	} else {
		query = query.Where("message_id = ?", event.MessageID)
	}
	var attempt CustomSMSSendLog
	if err := lockForUpdate(query).First(&attempt).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if err := tx.Model(&CustomSMSEvent{}).Where("fingerprint = ?", event.Fingerprint).Update("send_log_id", attempt.ID).Error; err != nil {
		return err
	}
	if attempt.DeliveryStatus == "delivered" || event.Time < attempt.ReportTime {
		return nil
	}
	status, code := "unknown", "report_unknown"
	switch {
	case event.Stat == "DELIVRD":
		status, code = "delivered", ""
	case slices.Contains([]string{"EXPIRED", "DELETED", "REJECTED", "MA:0001", "MA:0002", "MA:0003", "MA:0004", "MA:0005", "MA:0006", "MA:0009", "MA:0011", "MA:0015", "MA:0017", "MA:0021", "MA:0022", "MA:0023", "MA:0024", "MA:0025", "MA:0026", "MA:0027", "MA:0028", "MA:0029", "MA:0030", "MA:0033", "MA:0044", "MO:200", "MO:0254", "MO:0255"}, event.Stat):
		status, code = "failed", "delivery_failed"
	}
	return tx.Model(&attempt).Updates(map[string]any{"message_id": event.MessageID, "delivery_status": status, "error_code": code, "provider_stat": event.Stat, "report_time": event.Time, "reported_at": event.CreatedAt}).Error
}

// CompleteCustomSMSAttempt preserves a report that arrived before the submit response.
func CompleteCustomSMSAttempt(id int64, updates map[string]any) error {
	return DB.Session(&gorm.Session{Logger: DB.Logger.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
		var attempt CustomSMSSendLog
		if err := lockForUpdate(tx).First(&attempt, id).Error; err != nil {
			return err
		}
		if attempt.ReportTime != "" {
			delete(updates, "delivery_status")
			delete(updates, "error_code")
			if mid, ok := updates["message_id"].(string); ok && mid != attempt.MessageID {
				delete(updates, "message_id")
				updates["submit_status"] = "unknown"
			}
		}
		if err := tx.Model(&attempt).Updates(updates).Error; err != nil {
			return err
		}
		if err := tx.First(&attempt, id).Error; err != nil {
			return err
		}
		if attempt.MessageID == "" {
			return nil
		}
		var events []CustomSMSEvent
		if err := tx.Where("flag = ? AND account = ? AND message_id = ?", 1, attempt.Account, attempt.MessageID).Order("time, id").Find(&events).Error; err != nil {
			return err
		}
		for i := range events {
			if err := applyCustomSMSReport(tx, &events[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

type CustomSMSEvent struct {
	ID          int64  `json:"id"`
	Fingerprint string `json:"-" gorm:"size:64;uniqueIndex"`
	Flag        int    `json:"flag"`
	Account     string `json:"-" gorm:"size:128"`
	MessageID   string `json:"message_id" gorm:"size:128;index"`
	Label       string `json:"-" gorm:"size:128"`
	AccessCode  string `json:"access_code" gorm:"size:32"`
	Phone       string `json:"-" gorm:"size:32"`
	Content     string `json:"content" gorm:"type:text"`
	Stat        string `json:"stat" gorm:"size:80"`
	Time        string `json:"time" gorm:"size:19"`
	Source      string `json:"source" gorm:"size:16"`
	SendLogID   int64  `json:"send_log_id" gorm:"index"`
	CreatedAt   int64  `json:"created_at" gorm:"autoCreateTime:false"`
}
