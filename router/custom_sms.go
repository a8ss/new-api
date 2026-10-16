package router

import (
	"net/http"

	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
)

func registerCustomSMSRoutes(api *gin.RouterGroup) {
	routes := api.Group("/sms", middleware.AdminAuth(), middleware.TokenOperationAudit(), middleware.DisableCache())
	handlePermissionRoute(routes, http.MethodGet, "/tokens", authz.SMSRead, controller.GetCustomSMSTokens)
	handlePermissionRoute(routes, http.MethodGet, "/tokens/:id/logs", authz.SMSRead, controller.GetCustomSMSLogs)
	handlePermissionRoute(routes, http.MethodPost, "/tokens/:id/send", authz.SMSOperate, middleware.CriticalRateLimit(), controller.SendCustomSMS)
	handlePermissionRoute(routes, http.MethodGet, "/upstream", authz.SMSRead, controller.GetCustomSMSUpstream)
	handlePermissionRoute(routes, http.MethodPost, "/upstream/pull", authz.SMSOperate, middleware.CriticalRateLimit(), controller.PullCustomSMSUpstream)
}
