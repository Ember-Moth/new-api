// Package audit reads request metadata shared by management audit adapters.
package audit

import "github.com/gin-gonic/gin"

func OperatorInfo(c *gin.Context) map[string]any {
	method := "session"
	if c.GetBool("use_access_token") {
		method = "access_token"
	}
	return map[string]any{"admin_id": c.GetInt("id"), "admin_username": c.GetString("username"), "admin_role": c.GetInt("role"), "auth_method": method}
}
