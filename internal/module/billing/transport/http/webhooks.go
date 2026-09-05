package billinghttp

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/internal/module/billing/webhooks"
	"github.com/QuantumNous/new-api/logger"
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
