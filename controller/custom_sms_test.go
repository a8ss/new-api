package controller

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type customSMSFixture struct {
	t       *testing.T
	db      *gorm.DB
	router  *gin.Engine
	owner   model.User
	source  model.Token
	pat     string
	now     time.Time
	client  *service.CustomSMSService
	sent    []map[string]any
	respond func(http.ResponseWriter, map[string]any)
	mu      sync.Mutex
}

func newCustomSMSFixture(t *testing.T) *customSMSFixture {
	t.Helper()
	f := &customSMSFixture{t: t, now: time.Now().UTC()}
	previousDB, previousLog, previousSMS := model.DB, model.LOG_DB, service.CustomSMS
	previousMain, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
	previousRedis, previousSelf := common.RedisEnabled, operation_setting.SelfUseModeEnabled
	t.Cleanup(func() {
		model.DB, model.LOG_DB, service.CustomSMS = previousDB, previousLog, previousSMS
		common.SetDatabaseTypes(previousMain, previousLogType)
		common.RedisEnabled, operation_setting.SelfUseModeEnabled = previousRedis, previousSelf
	})
	kind, dsn, typ := "sqlite", "", common.DatabaseTypeSQLite
	switch os.Getenv("SMS_TEST_DB") {
	case "mysql":
		kind, dsn, typ = "mysql", os.Getenv("AUDIT_MYSQL_DSN"), common.DatabaseTypeMySQL
	case "postgres":
		kind, dsn, typ = "postgres", os.Getenv("AUDIT_POSTGRES_DSN"), common.DatabaseTypePostgreSQL
	}
	f.db, _ = newAuditTestDatabase(t, kind, dsn)
	model.DB, model.LOG_DB = f.db, f.db
	common.SetDatabaseTypes(typ, typ)
	connection, err := f.db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	common.RedisEnabled, operation_setting.SelfUseModeEnabled = false, true
	require.NoError(t, f.db.AutoMigrate(&model.Token{}, &model.User{}, &model.UserAccessToken{}, &model.UserSession{}, &model.Ability{}, &model.CustomSMSTemplate{}, &model.CustomSMSSendLog{}, &model.CustomSMSEvent{}))
	require.NoError(t, model.MigrateAuditLogs())
	t.Setenv("LOG_SQL_DSN", "")
	require.NoError(t, model.InitLogDB())
	require.NoError(t, f.db.AutoMigrate(&model.CasbinRule{}, &model.AuthzRole{}))
	oldMaster := common.IsMasterNode
	common.IsMasterNode = true
	t.Cleanup(func() { common.IsMasterNode = oldMaster })
	require.NoError(t, authz.Init(f.db))
	f.owner = model.User{Username: "sms-owner", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "sms-owner"}
	require.NoError(t, f.db.Create(&f.owner).Error)
	f.pat, _ = createScopedAccessToken(t, f.owner.Id, 0, "api_key:read", "api_key:write", "api_key:reveal")
	f.source = model.Token{UserId: f.owner.Id, Name: "sms-source", Key: "sms-disabled-source", Status: common.TokenStatusDisabled, RemainQuota: 100, ExpiredTime: -1}
	require.NoError(t, f.source.Insert())
	require.NoError(t, f.db.Create(&model.CustomSMSTemplate{SourceTokenID: f.source.Id, Content: "【new-api】{custom_share_url}"}).Error)
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := common.DecodeJson(r.Body, &request); !assert.NoError(t, err) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.sent = append(f.sent, request)
		sequence := len(f.sent)
		f.mu.Unlock()
		if f.respond != nil {
			f.respond(w, request)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"status":0,"list":[{"mid":"sms-mid-%d","mobile":"13800138000","result":0}]}`, sequence)
	}))
	t.Cleanup(provider.Close)
	t.Setenv("CUSTOM_SMS_TONGQIUBAO_URL", provider.URL+"/smsv2")
	t.Setenv("CUSTOM_SMS_TONGQIUBAO_ACCOUNT", "sms-account")
	t.Setenv("CUSTOM_SMS_TONGQIUBAO_PASSWORD", "sms-password")
	t.Setenv("CUSTOM_SMS_TONGQIUBAO_EXTNO", "10690")
	t.Setenv("CUSTOM_TOKEN_SHARE_BASE_URL", "https://keys.example.test")
	f.client = &service.CustomSMSService{Client: provider.Client(), Now: func() time.Time { return f.now }}
	service.CustomSMS = f.client
	f.router = gin.New()
	require.NoError(t, f.router.SetTrustedProxies(nil))
	routes := f.router.Group("/api/token", middleware.UserAuth(), middleware.TokenOperationAudit(), middleware.DisableCache())
	routes.POST("/derive", DeriveToken)
	routes.POST("/", AddToken)
	routes.GET("/:id/sms-logs", GetCustomSMSLogs)
	routes.POST("/:id/sms-send", SendCustomSMS)
	f.router.POST("/api/sms/tongqiubao/callback", ReceiveCustomSMSCallback)
	admin := f.router.Group("/api/sms", middleware.AdminAuth(), middleware.TokenOperationAudit())
	middleware.DeclareAccessTokenPermissionRoute(http.MethodGet, "/api/sms/tokens", authz.SMSRead)
	middleware.DeclareAccessTokenPermissionRoute(http.MethodGet, "/api/sms/tokens/:id/logs", authz.SMSRead)
	middleware.DeclareAccessTokenPermissionRoute(http.MethodPost, "/api/sms/tokens/:id/send", authz.SMSOperate)
	middleware.DeclareAccessTokenPermissionRoute(http.MethodGet, "/api/sms/upstream", authz.SMSRead)
	middleware.DeclareAccessTokenPermissionRoute(http.MethodPost, "/api/sms/upstream/pull", authz.SMSOperate)
	admin.GET("/tokens", middleware.RequirePermission(authz.SMSRead), GetCustomSMSTokens)
	admin.GET("/tokens/:id/logs", middleware.RequirePermission(authz.SMSRead), GetCustomSMSLogs)
	admin.POST("/tokens/:id/send", middleware.RequirePermission(authz.SMSOperate), SendCustomSMS)
	admin.GET("/upstream", middleware.RequirePermission(authz.SMSRead), GetCustomSMSUpstream)
	admin.POST("/upstream/pull", middleware.RequirePermission(authz.SMSOperate), PullCustomSMSUpstream)
	return f
}

func (f *customSMSFixture) request(method, path, credential, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Content-Type", "application/json")
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	result := httptest.NewRecorder()
	f.router.ServeHTTP(result, req)
	return result
}

func (f *customSMSFixture) derive(order string) customTokenDetails {
	f.t.Helper()
	result := f.request(http.MethodPost, "/api/token/derive", f.pat, fmt.Sprintf(`{"source_token_id":%d,"custom_order_no":%q,"custom_phone":"+86 13800138000"}`, f.source.Id, order))
	require.Equal(f.t, http.StatusOK, result.Code)
	var response struct {
		Success bool               `json:"success"`
		Data    customTokenDetails `json:"data"`
	}
	require.NoError(f.t, common.Unmarshal(result.Body.Bytes(), &response))
	require.True(f.t, response.Success, result.Body.String())
	return response.Data
}

func TestCustomSMSDeliveryLifecycle(t *testing.T) {
	f := newCustomSMSFixture(t)
	token := f.derive("sms-order")
	history := f.request(http.MethodGet, fmt.Sprintf("/api/token/%d/sms-logs", token.ID), f.pat, "")
	require.Equal(t, http.StatusOK, history.Code)
	assert.Contains(t, history.Body.String(), `"submit_status":"queued"`)
	require.NoError(t, f.client.ProcessPending(t.Context()))
	require.Len(t, f.sent, 1)
	assert.Equal(t, "p2p", f.sent[0]["action"])
	assert.Equal(t, "sms-password", f.sent[0]["password"])
	messages, ok := f.sent[0]["mobileContentKvp"].(map[string]any)
	require.True(t, ok)
	assert.Regexp(t, `^【new-api】https://keys\.example\.test/ck/[A-Za-z0-9]{8}$`, messages["13800138000"])
	history = f.request(http.MethodGet, fmt.Sprintf("/api/token/%d/sms-logs", token.ID), f.pat, "")
	assert.Contains(t, history.Body.String(), `"submit_status":"accepted"`)
	assert.Contains(t, history.Body.String(), `"delivery_status":"pending"`)
	assert.NotContains(t, history.Body.String(), "/ck/")
	assert.NotContains(t, history.Body.String(), token.Key)
	require.NoError(t, f.client.ProcessPending(t.Context()))
	assert.Len(t, f.sent, 1)
	report := fmt.Sprintf(`[{"flag":1,"mid":"sms-mid-1","spid":"sms-account","accessCode":"10690","mobile":"13800138000","stat":"DELIVRD","time":%q,"label":%q}]`, f.now.Format("2006-01-02 15:04:05"), f.sent[0]["label"])
	callback := f.request(http.MethodPost, "/api/sms/tongqiubao/callback", "", report)
	require.Equal(t, http.StatusOK, callback.Code)
	assert.JSONEq(t, `{"result":0}`, callback.Body.String())
	history = f.request(http.MethodGet, fmt.Sprintf("/api/token/%d/sms-logs", token.ID), f.pat, "")
	assert.Contains(t, history.Body.String(), `"delivery_status":"delivered"`)
}

func TestCustomSMSHTTPProviderSupportsSendAndUpstreamPull(t *testing.T) {
	f := newCustomSMSFixture(t)
	var actions []string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if !assert.NoError(t, common.DecodeJson(r.Body, &request)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		assert.Empty(t, r.URL.RawQuery)
		assert.Equal(t, "sms-account", request["account"])
		assert.Equal(t, "sms-password", request["password"])
		actions = append(actions, request["action"].(string))
		if request["action"] == "mo" {
			_, _ = w.Write([]byte(`{"status":0,"list":[]}`))
			return
		}
		assert.Equal(t, "p2p", request["action"])
		_, _ = w.Write([]byte(`{"status":0,"list":[{"mid":"http-mid","mobile":"13800138000","result":0}]}`))
	}))
	t.Cleanup(provider.Close)
	t.Setenv("CUSTOM_SMS_TONGQIUBAO_URL", provider.URL+"/smsv2")
	f.client.Client = provider.Client()
	token := f.derive("http-provider-order")
	require.NoError(t, f.client.ProcessPending(t.Context()))
	history := f.request(http.MethodGet, fmt.Sprintf("/api/token/%d/sms-logs", token.ID), f.pat, "")
	assert.Contains(t, history.Body.String(), `"submit_status":"accepted"`)
	count, code := f.client.PullUpstream(t.Context())
	assert.Empty(t, code)
	assert.Zero(t, count)
	assert.Equal(t, []string{"p2p", "mo"}, actions)
}

func TestCustomSMSManualSendIsIdempotentAndRateLimited(t *testing.T) {
	f := newCustomSMSFixture(t)
	f.respond = func(w http.ResponseWriter, _ map[string]any) { _, _ = w.Write([]byte(`{"status":3,"list":[]}`)) }
	token := f.derive("manual-order")
	require.NoError(t, f.client.ProcessPending(t.Context()))
	path := fmt.Sprintf("/api/token/%d/sms-send", token.ID)
	body := fmt.Sprintf(`{"idempotency_key":%q}`, uuid.NewString())
	first := f.request(http.MethodPost, path, f.pat, body)
	require.Equal(t, http.StatusAccepted, first.Code, first.Body.String())
	repeated := f.request(http.MethodPost, path, f.pat, body)
	assert.JSONEq(t, first.Body.String(), repeated.Body.String())
	require.NoError(t, f.client.ProcessPending(t.Context()))
	assert.Len(t, f.sent, 2)
	limited := f.request(http.MethodPost, path, f.pat, fmt.Sprintf(`{"idempotency_key":%q}`, uuid.NewString()))
	assert.Equal(t, http.StatusTooManyRequests, limited.Code)
}

func TestCustomSMSAdminViewsExcludeCredentialsAndOwnerCannotAccessOtherKeys(t *testing.T) {
	f := newCustomSMSFixture(t)
	token := f.derive("admin-order")
	admin := model.User{Username: "sms-admin", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "sms-admin"}
	require.NoError(t, f.db.Create(&admin).Error)
	adminPAT, _ := createScopedAccessToken(t, admin.Id, 0, "sms:read", "sms:operate")
	listing := f.request(http.MethodGet, "/api/sms/tokens", adminPAT, "")
	require.Equal(t, http.StatusOK, listing.Code, listing.Body.String())
	assert.Contains(t, listing.Body.String(), fmt.Sprintf(`"id":%d`, token.ID))
	assert.NotContains(t, listing.Body.String(), token.Key)
	assert.NotContains(t, listing.Body.String(), "/ck/")
	assert.Equal(t, http.StatusForbidden, f.request(http.MethodGet, "/api/sms/tokens", f.pat, "").Code)
}

func TestCustomSMSSupplierFailuresAreNotRetried(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{"account_rejected", `{"status":3}`, "failed", 200},
		{"recipient_rejected", `{"status":0,"list":[{"mobile":"13800138000","result":15}]}`, "failed", 200},
		{"missing_message_id", `{"status":0,"list":[{"mobile":"13800138000","result":0}]}`, "unknown", 200},
		{"invalid_json", `not json`, "unknown", 200},
		{"missing_status", `{"list":[]}`, "unknown", 200},
		{"upstream_http_error", `{}`, "unknown", 502},
		{"redirect", `{}`, "unknown", 307},
		{"connection_interrupted", "", "unknown", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCustomSMSFixture(t)
			f.respond = func(w http.ResponseWriter, _ map[string]any) {
				if tc.status == 0 {
					connection, _, err := w.(http.Hijacker).Hijack()
					if assert.NoError(t, err) {
						_ = connection.Close()
					}
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}
			token := f.derive("failure-order")
			require.NoError(t, f.client.ProcessPending(t.Context()))
			f.now = f.now.Add(time.Minute)
			require.NoError(t, f.client.ProcessPending(t.Context()))
			assert.Len(t, f.sent, 1)
			history := f.request(http.MethodGet, fmt.Sprintf("/api/token/%d/sms-logs", token.ID), f.pat, "")
			assert.Contains(t, history.Body.String(), `"submit_status":"`+tc.want+`"`)
		})
	}
}

func TestCustomSMSOnlyNewDerivationQueuesNotification(t *testing.T) {
	f := newCustomSMSFixture(t)
	created := f.request(http.MethodPost, "/api/token/", f.pat, `{"name":"ordinary-key","expired_time":-1,"remain_quota":10}`)
	require.Equal(t, http.StatusOK, created.Code)
	assert.Contains(t, created.Body.String(), `"success":true`)
	f.derive("unique-sms-order")
	duplicate := f.request(http.MethodPost, "/api/token/derive", f.pat, fmt.Sprintf(`{"source_token_id":%d,"custom_order_no":"unique-sms-order","custom_phone":"13800138000"}`, f.source.Id))
	assert.Contains(t, duplicate.Body.String(), `"success":false`)
	require.NoError(t, f.client.ProcessPending(t.Context()))
	assert.Len(t, f.sent, 1)
}

func TestCustomSMSMissingStorageDoesNotSendOrFailDerivation(t *testing.T) {
	f := newCustomSMSFixture(t)
	require.NoError(t, f.db.Migrator().DropTable(&model.CustomSMSSendLog{}))
	token := f.derive("no-log-order")
	assert.Positive(t, token.ID)
	assert.Empty(t, f.sent)
	// Recovery must still know the source without depending on a send log.
	require.NoError(t, f.db.AutoMigrate(&model.CustomSMSSendLog{}))
	sent := f.request(http.MethodPost, fmt.Sprintf("/api/token/%d/sms-send", token.ID), f.pat, fmt.Sprintf(`{"idempotency_key":%q}`, uuid.NewString()))
	require.Equal(t, http.StatusAccepted, sent.Code, sent.Body.String())
	require.NoError(t, f.client.ProcessPending(t.Context()))
	assert.Len(t, f.sent, 1)
}

func TestCustomSMSInvalidTemplatesLeaveKeyAvailable(t *testing.T) {
	for _, body := range []string{"", "{unknown}", "{custom_share_url"} {
		t.Run(body, func(t *testing.T) {
			f := newCustomSMSFixture(t)
			require.NoError(t, f.db.Model(&model.CustomSMSTemplate{}).Where("source_token_id = ?", f.source.Id).Update("content", body).Error)
			token := f.derive("invalid-template-order")
			require.NoError(t, f.client.ProcessPending(t.Context()))
			assert.Empty(t, f.sent)
			history := f.request(http.MethodGet, fmt.Sprintf("/api/token/%d/sms-logs", token.ID), f.pat, "")
			assert.Contains(t, history.Body.String(), `"error_code":"template_invalid"`)
		})
	}
}

func TestCustomSMSEarlyReportSurvivesSubmissionResponse(t *testing.T) {
	f := newCustomSMSFixture(t)
	f.respond = func(w http.ResponseWriter, r map[string]any) {
		report := fmt.Sprintf(`[{"flag":1,"mid":"early-mid","spid":"sms-account","accessCode":"10690","mobile":"13800138000","stat":"DELIVRD","time":%q,"label":%q}]`, f.now.Format("2006-01-02 15:04:05"), r["label"])
		result := f.request(http.MethodPost, "/api/sms/tongqiubao/callback", "", report)
		assert.Equal(t, http.StatusOK, result.Code)
		_, _ = w.Write([]byte(`{"status":0,"list":[{"mid":"early-mid","mobile":"13800138000","result":0}]}`))
	}
	token := f.derive("early-report-order")
	require.NoError(t, f.client.ProcessPending(t.Context()))
	history := f.request(http.MethodGet, fmt.Sprintf("/api/token/%d/sms-logs", token.ID), f.pat, "")
	assert.Contains(t, history.Body.String(), `"delivery_status":"delivered"`)
	assert.Contains(t, history.Body.String(), `"submit_status":"accepted"`)
}

func TestCustomSMSReportTimeoutAndLateReportStayOnOriginalAttempt(t *testing.T) {
	f := newCustomSMSFixture(t)
	token := f.derive("late-report-order")
	require.NoError(t, f.client.ProcessPending(t.Context()))
	f.now = f.now.Add(24 * time.Hour)
	require.NoError(t, f.client.ProcessPending(t.Context()))
	history := f.request(http.MethodGet, fmt.Sprintf("/api/token/%d/sms-logs", token.ID), f.pat, "")
	assert.Contains(t, history.Body.String(), `"delivery_status":"unknown"`)
	sent := f.request(http.MethodPost, fmt.Sprintf("/api/token/%d/sms-send", token.ID), f.pat, fmt.Sprintf(`{"idempotency_key":%q}`, uuid.NewString()))
	require.Equal(t, http.StatusAccepted, sent.Code)
	require.NoError(t, f.client.ProcessPending(t.Context()))
	report := fmt.Sprintf(`[{"flag":1,"mid":"sms-mid-1","spid":"sms-account","accessCode":"10690","mobile":"13800138000","stat":"DELIVRD","time":%q,"label":%q}]`, f.now.Format("2006-01-02 15:04:05"), f.sent[0]["label"])
	require.Equal(t, http.StatusOK, f.request(http.MethodPost, "/api/sms/tongqiubao/callback", "", report).Code)
	history = f.request(http.MethodGet, fmt.Sprintf("/api/token/%d/sms-logs", token.ID), f.pat, "")
	var result struct {
		Data struct {
			Items []model.CustomSMSSendLog `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(history.Body.Bytes(), &result))
	require.Len(t, result.Data.Items, 2)
	assert.Equal(t, "pending", result.Data.Items[0].DeliveryStatus)
	assert.Equal(t, "delivered", result.Data.Items[1].DeliveryStatus)
}

func TestCustomSMSPublicCallbackAndPullDeduplicateAndValidatePayload(t *testing.T) {
	f := newCustomSMSFixture(t)
	admin := model.User{Username: "sms-root", Role: common.RoleRootUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "sms-root"}
	require.NoError(t, f.db.Create(&admin).Error)
	pat, _ := createScopedAccessToken(t, admin.Id, 0, "sms:read", "sms:operate")
	key, err := common.GenerateKey()
	require.NoError(t, err)
	event := fmt.Sprintf(`{"flag":0,"mid":"upstream-mid","spid":"sms-account","accessCode":"10690","mobile":"13800138000","content":%q,"time":%q}`, "收到 "+key+" https://keys.example.test/ck/AbCd1234", f.now.Format("2006-01-02 15:04:05"))
	for _, address := range []string{"203.0.113.10:12345", "[2001:db8::1]:12345"} {
		request := httptest.NewRequest(http.MethodPost, "/api/sms/tongqiubao/callback", strings.NewReader("["+event+"]"))
		request.RemoteAddr = address
		response := httptest.NewRecorder()
		f.router.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		assert.JSONEq(t, `{"result":0}`, response.Body.String())
	}
	for _, invalid := range []string{
		strings.ReplaceAll(event, "sms-account", "other-account"),
		strings.ReplaceAll(event, "10690", "other-access-code"),
	} {
		assert.Equal(t, http.StatusBadRequest, f.request(http.MethodPost, "/api/sms/tongqiubao/callback", "", "["+invalid+"]").Code)
	}
	f.respond = func(w http.ResponseWriter, r map[string]any) {
		assert.Equal(t, "mo", r["action"])
		_, _ = fmt.Fprintf(w, `{"status":0,"list":[%s]}`, event)
	}
	pull := f.request(http.MethodPost, "/api/sms/upstream/pull", pat, "{}")
	require.Equal(t, http.StatusOK, pull.Code, pull.Body.String())
	list := f.request(http.MethodGet, "/api/sms/upstream", pat, "")
	require.Equal(t, http.StatusOK, list.Code)
	assert.Contains(t, list.Body.String(), `"total":1`)
	assert.Contains(t, list.Body.String(), "收到")
	assert.NotContains(t, list.Body.String(), "13800138000")
	assert.NotContains(t, list.Body.String(), key)
	assert.NotContains(t, list.Body.String(), "/ck/")
	var stored model.CustomSMSEvent
	require.NoError(t, f.db.First(&stored).Error)
	assert.NotContains(t, stored.Content, key)
	assert.Equal(t, http.StatusForbidden, f.request(http.MethodGet, "/api/sms/upstream", f.pat, "").Code)
}

func TestCustomSMSUnknownReportsRemainUnknown(t *testing.T) {
	for _, tc := range []struct{ stat, expected string }{{"MA:0001", "failed"}, {"MA:9999", "unknown"}, {"MO:0254", "failed"}, {"MO:9999", "unknown"}, {"REJECTD", "unknown"}} {
		t.Run(tc.stat, func(t *testing.T) {
			f := newCustomSMSFixture(t)
			token := f.derive("report-classification")
			require.NoError(t, f.client.ProcessPending(t.Context()))
			event := fmt.Sprintf(`[{"flag":1,"mid":"sms-mid-1","spid":"sms-account","accessCode":"10690","mobile":"13800138000","stat":%q,"time":%q,"label":%q}]`, tc.stat, f.now.Format("2006-01-02 15:04:05"), f.sent[0]["label"])
			require.Equal(t, http.StatusOK, f.request(http.MethodPost, "/api/sms/tongqiubao/callback", "", event).Code)
			history := f.request(http.MethodGet, fmt.Sprintf("/api/token/%d/sms-logs", token.ID), f.pat, "")
			assert.Contains(t, history.Body.String(), `"delivery_status":"`+tc.expected+`"`)
			assert.Contains(t, history.Body.String(), `"provider_stat":"`+tc.stat+`"`)
		})
	}
}

func TestCustomSMSLatestUnknownIsIndependentOfHistoryPage(t *testing.T) {
	f := newCustomSMSFixture(t)
	token := f.derive("pagination-order")
	require.NoError(t, f.db.Model(&model.CustomSMSSendLog{}).Where("token_id = ?", token.ID).Updates(map[string]any{"submit_status": "unknown"}).Error)
	history := f.request(http.MethodGet, fmt.Sprintf("/api/token/%d/sms-logs?p=2&page_size=1", token.ID), f.pat, "")
	require.Equal(t, http.StatusOK, history.Code)
	assert.Contains(t, history.Body.String(), `"latest_unknown":true`)
}

func TestCustomSMSManualPermissionsAndSharedDailyLimit(t *testing.T) {
	f := newCustomSMSFixture(t)
	f.respond = func(w http.ResponseWriter, _ map[string]any) { _, _ = w.Write([]byte(`{"status":3}`)) }
	token := f.derive("permissions-order")
	require.NoError(t, f.client.ProcessPending(t.Context()))
	path := fmt.Sprintf("/api/token/%d/sms-send", token.ID)
	body := fmt.Sprintf(`{"idempotency_key":%q}`, uuid.NewString())
	readOnly, _ := createScopedAccessToken(t, f.owner.Id, 0, "api_key:read", "api_key:write")
	assert.Equal(t, http.StatusForbidden, f.request(http.MethodPost, path, readOnly, body).Code)
	assert.Equal(t, http.StatusUnauthorized, f.request(http.MethodGet, fmt.Sprintf("/api/token/%d/sms-logs", token.ID), token.Key, "").Code)
	other := model.User{Username: "sms-other", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "sms-other"}
	require.NoError(t, f.db.Create(&other).Error)
	otherPAT, _ := createScopedAccessToken(t, other.Id, 0, "api_key:read", "api_key:write", "api_key:reveal")
	assert.Equal(t, http.StatusNotFound, f.request(http.MethodGet, fmt.Sprintf("/api/token/%d/sms-logs", token.ID), otherPAT, "").Code)
	assert.Equal(t, http.StatusNotFound, f.request(http.MethodPost, path, otherPAT, body).Code)
	admin := model.User{Username: "sms-limit-admin", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "sms-limit-admin"}
	require.NoError(t, f.db.Create(&admin).Error)
	adminPAT, _ := createScopedAccessToken(t, admin.Id, 0, "sms:read", "sms:operate")
	// Keep the fixture inside one natural day, alternating the actual actors.
	f.now = time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	for i := range 5 {
		credential, endpoint := f.pat, path
		if i%2 == 1 {
			credential, endpoint = adminPAT, fmt.Sprintf("/api/sms/tokens/%d/send", token.ID)
		}
		body = fmt.Sprintf(`{"idempotency_key":%q}`, uuid.NewString())
		result := f.request(http.MethodPost, endpoint, credential, body)
		require.Equal(t, http.StatusAccepted, result.Code, result.Body.String())
		require.NoError(t, f.client.ProcessPending(t.Context()))
		f.now = f.now.Add(time.Minute)
	}
	assert.Len(t, f.sent, 6)
	limited := f.request(http.MethodPost, path, f.pat, fmt.Sprintf(`{"idempotency_key":%q}`, uuid.NewString()))
	assert.Equal(t, http.StatusTooManyRequests, limited.Code)
	f.now = f.now.AddDate(0, 0, 1)
	assert.Equal(t, http.StatusAccepted, f.request(http.MethodPost, path, f.pat, fmt.Sprintf(`{"idempotency_key":%q}`, uuid.NewString())).Code)
}

func TestCustomSMSConcurrentManualAndWorkerClaims(t *testing.T) {
	f := newCustomSMSFixture(t)
	f.respond = func(w http.ResponseWriter, _ map[string]any) { _, _ = w.Write([]byte(`{"status":3}`)) }
	token := f.derive("concurrent-order")
	require.NoError(t, f.client.ProcessPending(t.Context()))
	path := fmt.Sprintf("/api/token/%d/sms-send", token.ID)
	body := fmt.Sprintf(`{"idempotency_key":%q}`, uuid.NewString())
	var responses [2]*httptest.ResponseRecorder
	var wait sync.WaitGroup
	for i := range 2 {
		wait.Go(func() { responses[i] = f.request(http.MethodPost, path, f.pat, body) })
	}
	wait.Wait()
	for _, response := range responses {
		require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	}
	assert.JSONEq(t, responses[0].Body.String(), responses[1].Body.String())
	errors := make(chan error, 2)
	for range 2 {
		wait.Go(func() { errors <- f.client.ProcessPending(t.Context()) })
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	assert.Len(t, f.sent, 2)
}

func TestCustomSMSRevalidatesCurrentTemplateAndShareBeforeSending(t *testing.T) {
	f := newCustomSMSFixture(t)
	token := f.derive("current-template-order")
	revoked := f.derive("revoked-order")
	require.NoError(t, f.db.Model(&model.Token{}).Where("id = ?", revoked.ID).Update("custom_share_code", nil).Error)
	require.NoError(t, f.db.Model(&model.CustomSMSTemplate{}).Where("source_token_id = ?", f.source.Id).Update("content", "新版 {custom_share_url}").Error)
	require.NoError(t, f.db.Delete(&f.source).Error)
	require.NoError(t, f.client.ProcessPending(t.Context()))
	require.Len(t, f.sent, 1)
	assert.Contains(t, f.sent[0]["mobileContentKvp"].(map[string]any)["13800138000"], "新版 https://")
	token = revoked
	require.NoError(t, f.client.ProcessPending(t.Context()))
	assert.Len(t, f.sent, 1)
	result := f.request(http.MethodPost, fmt.Sprintf("/api/token/%d/sms-send", token.ID), f.pat, fmt.Sprintf(`{"idempotency_key":%q}`, uuid.NewString()))
	assert.Equal(t, http.StatusBadRequest, result.Code)
	assert.Contains(t, result.Body.String(), "share_unavailable")
}

func TestCustomSMSInterruptedClaimDoesNotResumeAndCallbackRequiresPersistence(t *testing.T) {
	f := newCustomSMSFixture(t)
	token := f.derive("interrupted-order")
	require.NoError(t, f.db.Model(&model.CustomSMSSendLog{}).Where("token_id = ?", token.ID).Updates(map[string]any{"submit_status": "sending", "claimed_at": f.now.Add(-time.Minute).Unix()}).Error)
	require.NoError(t, f.client.ProcessPending(t.Context()))
	require.NoError(t, f.client.ProcessPending(t.Context()))
	assert.Empty(t, f.sent)
	history := f.request(http.MethodGet, fmt.Sprintf("/api/token/%d/sms-logs", token.ID), f.pat, "")
	assert.Contains(t, history.Body.String(), "process_interrupted")
	event := fmt.Sprintf(`[{"flag":1,"mid":"unmatched","spid":"sms-account","accessCode":"10690","mobile":"13800138000","stat":"DELIVRD","time":%q}]`, f.now.Format("2006-01-02 15:04:05"))
	require.NoError(t, f.db.Migrator().DropTable(&model.CustomSMSEvent{}))
	result := f.request(http.MethodPost, "/api/sms/tongqiubao/callback", "", event)
	assert.Equal(t, http.StatusServiceUnavailable, result.Code)
	assert.JSONEq(t, `{"result":1}`, result.Body.String())
}

func TestCustomSMSLeaseStartsWhenEachAttemptIsClaimed(t *testing.T) {
	f := newCustomSMSFixture(t)
	f.derive("batch-first")
	second := f.derive("batch-second")
	f.respond = func(w http.ResponseWriter, _ map[string]any) {
		if len(f.sent) == 1 {
			// Advance at the clock boundary to represent earlier work in the batch.
			f.now = f.now.Add(time.Minute)
		} else {
			if !assert.NoError(t, f.client.ProcessPending(t.Context())) {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			history := f.request(http.MethodGet, fmt.Sprintf("/api/token/%d/sms-logs", second.ID), f.pat, "")
			assert.Contains(t, history.Body.String(), `"submit_status":"sending"`)
			assert.NotContains(t, history.Body.String(), "process_interrupted")
		}
		_, _ = w.Write([]byte(`{"status":3}`))
	}
	require.NoError(t, f.client.ProcessPending(t.Context()))
}

func TestCustomSMSMigrationMatrix(t *testing.T) {
	for _, database := range []struct{ name, env string }{{"sqlite", ""}, {"mysql", "AUDIT_MYSQL_DSN"}, {"postgres", "AUDIT_POSTGRES_DSN"}} {
		for _, upgrade := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/upgrade=%v", database.name, upgrade), func(t *testing.T) {
				dsn := os.Getenv(database.env)
				if database.env != "" && dsn == "" {
					t.Skip(database.env + " is not configured")
				}
				db, _ := newAuditTestDatabase(t, database.name, dsn)
				connection, err := db.DB()
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, connection.Close()) })
				// The released token schema is unchanged between rc.40 and rc.44.
				if upgrade {
					require.NoError(t, db.AutoMigrate(&customReleasedToken{}))
					require.NoError(t, db.Create(&customReleasedToken{Id: 41, Key: "release-sms-key", Name: "legacy", RemainQuota: 123, AutoGroups: `["default"]`}).Error)
				}
				for range 2 {
					require.NoError(t, db.AutoMigrate(&model.Token{}, &model.CustomSMSTemplate{}, &model.CustomSMSSendLog{}, &model.CustomSMSEvent{}))
				}
				var migrationLog bytes.Buffer
				traced := db.Session(&gorm.Session{Logger: logger.New(log.New(&migrationLog, "", 0), logger.Config{LogLevel: logger.Info})})
				require.NoError(t, traced.AutoMigrate(&model.Token{}, &model.CustomSMSTemplate{}, &model.CustomSMSSendLog{}, &model.CustomSMSEvent{}))
				for _, ddl := range []string{"ALTER TABLE", "CREATE TABLE", "CREATE INDEX", "CREATE UNIQUE INDEX", "DROP "} {
					assert.NotContains(t, strings.ToUpper(migrationLog.String()), ddl)
				}
				if upgrade {
					var old model.Token
					require.NoError(t, db.First(&old, 41).Error)
					assert.Equal(t, "release-sms-key", old.Key)
					assert.Equal(t, 123, old.RemainQuota)
					assert.Equal(t, `["default"]`, old.AutoGroups)
					assert.Nil(t, old.CustomSourceTokenID)
					assert.Error(t, db.Create(&model.Token{Key: old.Key}).Error)
				}
				source := model.Token{Key: "sms-source-migration"}
				require.NoError(t, db.Create(&source).Error)
				template := model.CustomSMSTemplate{SourceTokenID: source.Id, Content: "{custom_share_url}"}
				require.NoError(t, db.Create(&template).Error)
				assert.Error(t, db.Create(&template).Error)
				derived := model.Token{Key: "sms-derived-migration", CustomSourceTokenID: &source.Id}
				require.NoError(t, db.Create(&derived).Error)
				entry := model.CustomSMSSendLog{TokenID: derived.Id, SourceTokenID: source.Id, RequestID: uuid.NewString(), IdempotencyKey: "same-request", SubmitStatus: "failed"}
				require.NoError(t, db.Create(&entry).Error)
				entry.ID, entry.RequestID = 0, uuid.NewString()
				assert.Error(t, db.Create(&entry).Error)
				event := model.CustomSMSEvent{Fingerprint: "same-event"}
				require.NoError(t, db.Create(&event).Error)
				event.ID = 0
				assert.Error(t, db.Create(&event).Error)
				require.NoError(t, db.Unscoped().Delete(&source).Error)
				require.NoError(t, db.First(&derived, derived.Id).Error)
				assert.Equal(t, source.Id, *derived.CustomSourceTokenID)
				require.NoError(t, db.First(&template, "source_token_id = ?", source.Id).Error)
				require.NoError(t, db.Where("token_id = ?", derived.Id).First(&entry).Error)
				versionSQL := "SELECT version()"
				if database.name == "sqlite" {
					versionSQL = "SELECT sqlite_version()"
				}
				var version string
				require.NoError(t, db.Raw(versionSQL).Scan(&version).Error)
				t.Logf("database version: %s", version)
			})
		}
	}
}
