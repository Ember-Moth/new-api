package setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeStripePaymentIntentPaymentMethodTypes(t *testing.T) {
	normalized, err := NormalizeStripePaymentIntentPaymentMethodTypes(" card, Alipay\nwechat_pay;card ")

	require.NoError(t, err)
	assert.Equal(t, "card,alipay,wechat_pay", normalized)
}

func TestNormalizeStripePaymentIntentPaymentMethodTypesRejectsInvalidValue(t *testing.T) {
	_, err := NormalizeStripePaymentIntentPaymentMethodTypes("card,alipay!")

	require.Error(t, err)
}
