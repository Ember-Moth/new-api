package billinghttp

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/module/billing/webhooks"
	"github.com/QuantumNous/new-api/internal/infra/logger"
	"github.com/gin-gonic/gin"
)

func (h *Handler) StripeWebhook(c *gin.Context) { h.paymentWebhook(c, "stripe") }
func (h *Handler) CreemWebhook(c *gin.Context)  { h.paymentWebhook(c, "creem") }

func (h *Handler) paymentWebhook(c *gin.Context, provider string) {
	if !h.billing.Webhooks.Enabled(provider) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	payload, err := io.ReadAll(c.Request.Body)
	if err != nil {
		status := http.StatusBadRequest
		if provider == "stripe" {
			status = http.StatusServiceUnavailable
		}
		c.AbortWithStatus(status)
		return
	}
	if provider == "stripe" {
		err = h.billing.Webhooks.Stripe(c.Request.Context(), payload, c.GetHeader("Stripe-Signature"), c.ClientIP())
	} else {
		err = h.billing.Webhooks.Creem(c.Request.Context(), payload, c.GetHeader("creem-signature"), c.ClientIP())
	}
	if err == nil {
		c.Status(http.StatusOK)
		return
	}
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, webhooks.ErrDisabled):
		status = http.StatusForbidden
	case errors.Is(err, webhooks.ErrPayload):
		status = http.StatusBadRequest
	case errors.Is(err, webhooks.ErrSignature):
		status = http.StatusBadRequest
		if provider == "creem" {
			status = http.StatusUnauthorized
		}
	}
	logger.LogWarn(c.Request.Context(), fmt.Sprintf("payment webhook failed provider=%s error=%v", provider, err))
	c.AbortWithStatus(status)
}

func (h *Handler) WaffoWebhook(c *gin.Context) {
	if !h.billing.Webhooks.Enabled("waffo") {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	payload, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	response, err := h.billing.Webhooks.Waffo(c.Request.Context(), payload, c.GetHeader("X-SIGNATURE"), c.ClientIP())
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("payment webhook failed provider=waffo error=%v", err))
	}
	if response != nil {
		c.Header("X-SIGNATURE", response.Signature)
		c.Data(http.StatusOK, "application/json", response.Body)
		return
	}
	status := http.StatusInternalServerError
	if errors.Is(err, webhooks.ErrDisabled) {
		status = http.StatusForbidden
	} else if errors.Is(err, webhooks.ErrSignature) {
		status = http.StatusBadRequest
	}
	c.AbortWithStatus(status)
}

func (h *Handler) WaffoPancakeWebhook(c *gin.Context) {
	if !h.billing.Webhooks.Enabled("waffo_pancake") {
		c.String(http.StatusForbidden, "webhook disabled")
		return
	}
	environment := strings.TrimSpace(c.Param("env"))
	if environment != "test" && environment != "prod" {
		c.String(http.StatusNotFound, "unknown env")
		return
	}
	payload, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.String(http.StatusBadRequest, "bad request")
		return
	}
	err = h.billing.Webhooks.Pancake(c.Request.Context(), payload, c.GetHeader("X-Waffo-Signature"), environment, c.ClientIP())
	if err == nil {
		c.String(http.StatusOK, "OK")
		return
	}
	logger.LogWarn(c.Request.Context(), fmt.Sprintf("payment webhook failed provider=waffo_pancake error=%v", err))
	switch {
	case errors.Is(err, webhooks.ErrDisabled):
		c.String(http.StatusForbidden, "webhook disabled")
	case errors.Is(err, webhooks.ErrEnvironment):
		c.String(http.StatusNotFound, "unknown env")
	case errors.Is(err, webhooks.ErrSignature):
		c.String(http.StatusUnauthorized, "invalid signature")
	case errors.Is(err, webhooks.ErrOrderIdentity), errors.Is(err, contract.ErrPaymentMethodMismatch):
		c.String(http.StatusOK, "OK")
	default:
		c.String(http.StatusInternalServerError, "retry")
	}
}
