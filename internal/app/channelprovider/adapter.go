package channelprovider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/common"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/internal/module/channel"
	"github.com/QuantumNous/new-api/internal/module/channel/contract"
	relaychannel "github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/advancedcustom"
	"github.com/QuantumNous/new-api/relay/channel/gemini"
	"github.com/QuantumNous/new-api/relay/channel/ollama"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/internal/legacy/service"
)

type Adapter struct{}

func (Adapter) RefreshCredential(ctx context.Context, id int) (*contract.RefreshedCredential, error) {
	key, ch, err := service.RefreshCodexChannelCredential(ctx, id, service.CodexCredentialRefreshOptions{ResetCaches: true})
	if err != nil {
		return nil, err
	}
	return &contract.RefreshedCredential{ExpiresAt: key.Expired, LastRefresh: key.LastRefresh, AccountID: key.AccountID, Email: key.Email, ChannelID: ch.Id, ChannelType: ch.Type, ChannelName: ch.Name}, nil
}

func (Adapter) PullModel(ctx context.Context, baseURL, key, name string, progress func([]byte)) error {
	if progress == nil {
		return ollama.PullOllamaModel(baseURL, key, name)
	}
	return ollama.PullOllamaModelStream(baseURL, key, name, func(update ollama.OllamaPullResponse) {
		data, err := common.Marshal(update)
		if err == nil {
			progress(data)
		}
	})
}

func (Adapter) DeleteModel(ctx context.Context, baseURL, key, name string) error {
	return ollama.DeleteOllamaModel(baseURL, key, name)
}

func (Adapter) ModelServerVersion(ctx context.Context, baseURL, key string) (string, error) {
	return ollama.FetchOllamaVersion(baseURL, key)
}

func (Adapter) ApplyHeaderOverrides(ch *channel.Channel, key string, headers http.Header) error {
	info := &relaycommon.RelayInfo{IsChannelTest: true, ChannelMeta: &relaycommon.ChannelMeta{ApiKey: key, HeadersOverride: ch.GetHeaderOverride()}}
	overrides, err := relaychannel.ResolveHeaderOverride(info, nil)
	if err != nil {
		return err
	}
	for name, value := range overrides {
		headers.Set(name, value)
	}
	return nil
}

func (Adapter) AdvancedRequest(ch *channel.Channel, baseURL, key, path string) (string, http.Header, error) {
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeUnknown, RequestURLPath: path,
		ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeAdvancedCustom, ChannelBaseUrl: baseURL, ApiKey: key, ChannelOtherSettings: ch.GetOtherSettings()},
	}
	adapter := &advancedcustom.Adaptor{}
	if path == dto.AdvancedCustomBalancePath {
		return adapter.BuildBalanceRequest(info)
	}
	return adapter.BuildModelListRequest(info)
}

func (Adapter) NativeModels(ctx context.Context, ch *channel.Channel, baseURL, key string) ([]string, error) {
	switch ch.Type {
	case constant.ChannelTypeOllama:
		models, err := ollama.FetchOllamaModels(baseURL, key)
		if err != nil {
			return nil, err
		}
		result := make([]string, 0, len(models))
		for _, model := range models {
			result = append(result, model.Name)
		}
		return result, nil
	case constant.ChannelTypeGemini:
		return gemini.FetchGeminiModels(baseURL, key, ch.GetSetting().Proxy)
	case constant.ChannelTypeCodex:
		return service.FetchCodexChannelModels(ch)
	default:
		return nil, fmt.Errorf("unsupported native model discovery for channel type %d", ch.Type)
	}
}
