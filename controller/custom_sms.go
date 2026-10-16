package controller

import (
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type customSMSLogResponse struct {
	*model.CustomSMSSendLog
	Phone string `json:"phone"`
}

func ReceiveCustomSMSCallback(c *gin.Context) {
	if os.Getenv("CUSTOM_SMS_TONGQIUBAO_ACCOUNT") == "" || os.Getenv("CUSTOM_SMS_TONGQIUBAO_EXTNO") == "" {
		c.JSON(http.StatusForbidden, gin.H{"result": 1})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20))
	var events []service.CustomSMSProviderEvent
	if err != nil || common.Unmarshal(body, &events) != nil || len(events) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"result": 1})
		return
	}
	if err := service.CustomSMS.ReceiveEvents(events, "push"); err != nil {
		status := http.StatusBadRequest
		if err.Error() == "storage_error" {
			status = http.StatusServiceUnavailable
		}
		c.JSON(status, gin.H{"result": 1})
		return
	}
	c.JSON(http.StatusOK, gin.H{"result": 0})
}

func customSMSManagedToken(c *gin.Context) (*model.Token, error) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		return nil, gorm.ErrRecordNotFound
	}
	if strings.HasPrefix(c.FullPath(), "/api/sms/") && c.GetInt("role") >= common.RoleAdminUser {
		return model.GetTokenById(id)
	}
	return model.GetTokenByIds(id, c.GetInt("id"))
}

func GetCustomSMSLogs(c *gin.Context) {
	token, err := customSMSManagedToken(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "API key is unavailable"})
		return
	}
	page := common.GetPageQuery(c)
	page.PageSize = max(1, page.PageSize)
	page.Page = max(1, page.Page)
	db := model.DB.Session(&gorm.Session{Logger: model.DB.Logger.LogMode(logger.Silent)})
	query := db.Model(&model.CustomSMSSendLog{}).Where("token_id = ?", token.Id)
	var total int64
	var logs []model.CustomSMSSendLog
	if err := query.Count(&total).Error; err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "SMS storage is unavailable"})
		return
	}
	if err := query.Order("id DESC").Limit(page.PageSize).Offset(page.GetStartIdx()).Find(&logs).Error; err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "SMS storage is unavailable"})
		return
	}
	items := make([]customSMSLogResponse, 0, len(logs))
	for i := range logs {
		items = append(items, customSMSLogResponse{CustomSMSSendLog: &logs[i], Phone: "****" + logs[i].Phone[max(0, len(logs[i].Phone)-4):]})
	}
	var latest model.CustomSMSSendLog
	if total > 0 {
		if err := db.Select("submit_status", "delivery_status").Where("token_id = ?", token.Id).Order("id DESC").First(&latest).Error; err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "SMS storage is unavailable"})
			return
		}
	}
	reason := service.CustomSMS.ManualBlockReason(token)
	common.ApiSuccess(c, gin.H{"items": items, "total": total, "page": page.Page, "page_size": page.PageSize, "can_send": reason == "", "send_error": reason, "latest_unknown": latest.SubmitStatus == "unknown" || latest.DeliveryStatus == "unknown"})
}

func SendCustomSMS(c *gin.Context) {
	token, err := customSMSManagedToken(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "API key is unavailable"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 4096))
	var request map[string]common.RawMessage
	var key string
	if err != nil || common.Unmarshal(body, &request) != nil || len(request) != 1 || common.Unmarshal(request["idempotency_key"], &key) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "request_invalid"})
		return
	}
	parsed, err := uuid.Parse(key)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "request_invalid"})
		return
	}
	result, code := service.CustomSMS.QueueManual(token, c.GetInt("id"), parsed.String())
	if code != "" {
		status := http.StatusBadRequest
		switch code {
		case "storage_error":
			status = http.StatusServiceUnavailable
		case "rate_limited":
			status = http.StatusTooManyRequests
		case "send_in_progress":
			status = http.StatusConflict
		}
		c.JSON(status, gin.H{"success": false, "message": code})
		return
	}
	params := tokenAuditParams(c)
	params["id"], params["sms_log_id"] = token.Id, result.LogID
	c.JSON(http.StatusAccepted, gin.H{"success": true, "data": result})
}

func GetCustomSMSTokens(c *gin.Context) {
	page := common.GetPageQuery(c)
	page.Page, page.PageSize = max(1, page.Page), max(1, page.PageSize)
	db := model.DB.Session(&gorm.Session{Logger: model.DB.Logger.LogMode(logger.Silent)})
	query := db.Model(&model.Token{}).Where("custom_source_token_id IS NOT NULL OR custom_order_no IS NOT NULL")
	if raw := c.Query("token_id"); raw != "" {
		id, err := strconv.Atoi(raw)
		if err != nil || id <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "request_invalid"})
			return
		}
		query = query.Where("id = ?", id)
	}
	var total int64
	var tokens []model.Token
	if err := query.Count(&total).Error; err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "SMS storage is unavailable"})
		return
	}
	if err := query.Select("id", "name", "user_id", "custom_source_token_id", "custom_phone").Order("id DESC").Offset(page.GetStartIdx()).Limit(page.PageSize).Find(&tokens).Error; err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "SMS storage is unavailable"})
		return
	}
	type item struct {
		ID            int    `json:"id"`
		Name          string `json:"name"`
		UserID        int    `json:"user_id"`
		SourceTokenID *int   `json:"source_token_id"`
		Phone         string `json:"phone"`
	}
	items := make([]item, 0, len(tokens))
	for _, token := range tokens {
		phone := service.NormalizeCustomSMSPhone(token.CustomPhone)
		items = append(items, item{ID: token.Id, Name: token.Name, UserID: token.UserId, SourceTokenID: token.CustomSourceTokenID, Phone: "****" + phone[max(0, len(phone)-4):]})
	}
	common.ApiSuccess(c, gin.H{"items": items, "total": total, "page": page.Page, "page_size": page.PageSize})
}

func GetCustomSMSUpstream(c *gin.Context) {
	page := common.GetPageQuery(c)
	page.Page, page.PageSize = max(1, page.Page), max(1, page.PageSize)
	db := model.DB.Session(&gorm.Session{Logger: model.DB.Logger.LogMode(logger.Silent)})
	query := db.Model(&model.CustomSMSEvent{}).Where("flag = ?", 0)
	var total int64
	var events []model.CustomSMSEvent
	if err := query.Count(&total).Error; err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "SMS storage is unavailable"})
		return
	}
	if err := query.Order("id DESC").Offset(page.GetStartIdx()).Limit(page.PageSize).Find(&events).Error; err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "SMS storage is unavailable"})
		return
	}
	type item struct {
		*model.CustomSMSEvent
		Phone string `json:"phone"`
	}
	items := make([]item, 0, len(events))
	for i := range events {
		items = append(items, item{CustomSMSEvent: &events[i], Phone: "****" + events[i].Phone[max(0, len(events[i].Phone)-4):]})
	}
	common.ApiSuccess(c, gin.H{"items": items, "total": total, "page": page.Page, "page_size": page.PageSize})
}

func PullCustomSMSUpstream(c *gin.Context) {
	count, code := service.CustomSMS.PullUpstream(c.Request.Context())
	if code != "" {
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": code})
		return
	}
	common.ApiSuccess(c, gin.H{"received": count})
}
