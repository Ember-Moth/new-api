package controller

import (
	"github.com/QuantumNous/new-api/internal/legacy/model"
	channelmodule "github.com/QuantumNous/new-api/internal/module/channel"
	"github.com/QuantumNous/new-api/relaykit/types"
)

func newChannelError(channel *model.Channel, usingKey string) types.ChannelError {
	if channel == nil {
		return types.ChannelError{}
	}
	error := types.NewChannelError(channel.Id, channel.Type, channel.Name, channel.ChannelInfo.IsMultiKey, usingKey, channel.GetAutoBan())
	error.KeyPoolFingerprint = channelmodule.ChannelKeyPoolFingerprint(channel)
	return *error
}
