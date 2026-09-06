package paymentconfig

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	pancake "github.com/waffo-com/waffo-pancake-sdk-go"
)

type Config struct{ MerchantID, PrivateKey, ReturnURL, StoreID, ProductID string }
type Dependencies struct {
	TermsVersion string
	Config       func() Config
	SaveOptions  func(context.Context, map[string]string) error
	HTTPClient   *http.Client
	BaseURL      string
}
type Service struct{ deps Dependencies }

func New(deps Dependencies) *Service     { return &Service{deps: deps} }
func (s *Service) Configuration() Config { return s.deps.Config() }

// ResolveCredentials uses saved credentials only when both supplied fields are blank.
func (s *Service) ResolveCredentials(merchant, private string) (string, string) {
	merchant, private = strings.TrimSpace(merchant), strings.TrimSpace(private)
	if merchant == "" && private == "" {
		cfg := s.deps.Config()
		return cfg.MerchantID, cfg.PrivateKey
	}
	return merchant, private
}
func (s *Service) client(merchantID, privateKey string) (*pancake.Client, error) {
	if strings.TrimSpace(merchantID) == "" || strings.TrimSpace(privateKey) == "" {
		return nil, fmt.Errorf("merchant id and private key are required")
	}
	return pancake.New(pancake.Config{MerchantID: merchantID, PrivateKey: privateKey, HTTPClient: s.deps.HTTPClient, BaseURL: s.deps.BaseURL})
}

// Deterministic default names for "+ Create": stable bodies mean stable
// X-Idempotency-Key, which lets Pancake dedupe retries server-side.
const (
	defaultWaffoPancakeStoreName   = "new-api-store"
	defaultWaffoPancakeProductName = "new-api-charge-product"
)

// CreatePrimaryStore creates a Pancake Store using in-flight
// (not-yet-persisted) credentials and returns the new store ID.
func (s *Service) CreatePrimaryStore(ctx context.Context, merchantID, privateKey string) (string, error) {
	client, err := s.client(merchantID, privateKey)
	if err != nil {
		return "", err
	}
	storeRes, err := client.Stores.Create(ctx, pancake.CreateStoreParams{
		Name: defaultWaffoPancakeStoreName,
	})
	if err != nil {
		return "", fmt.Errorf("create Waffo Pancake store: %w", err)
	}
	if storeRes == nil || strings.TrimSpace(storeRes.Store.ID) == "" {
		return "", fmt.Errorf("Waffo Pancake returned empty store")
	}
	return storeRes.Store.ID, nil
}

// CreateProductForPlan mints (and publishes) a Pancake
// OnetimeProduct priced at `amount` USD, used as a subscription plan's
// SubscriptionPlan.WaffoPancakeProductId.
//
// OnetimeProduct (not SubscriptionProduct) because new-api has no renewal-
// event handling; Pancake auto-renewing without new-api extending user
// access would be a UX divergence. Revisit if renewal handling is added.
func (s *Service) CreateProductForPlan(ctx context.Context, merchantID, privateKey, storeID, name, amount, returnURL string) (string, error) {
	storeID = strings.TrimSpace(storeID)
	if storeID == "" {
		return "", fmt.Errorf("store id is required to create a product")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("plan name is required")
	}
	amount = strings.TrimSpace(amount)
	if amount == "" {
		return "", fmt.Errorf("plan price is required")
	}
	client, err := s.client(merchantID, privateKey)
	if err != nil {
		return "", err
	}
	var successURL *string
	if value := strings.TrimSpace(returnURL); value != "" {
		successURL = &value
	}
	prodRes, err := client.OnetimeProducts.Create(ctx, pancake.CreateOnetimeProductParams{
		StoreID: storeID,
		Name:    name,
		Prices: pancake.Prices{
			"USD": {
				Amount:      amount,
				TaxCategory: pancake.TaxCategory("saas"),
			},
		},
		SuccessURL: successURL,
	})
	if err != nil {
		return "", fmt.Errorf("create Waffo Pancake plan product: %w", err)
	}
	if prodRes == nil || strings.TrimSpace(prodRes.Product.ID) == "" {
		return "", fmt.Errorf("Waffo Pancake returned empty product")
	}
	productID := prodRes.Product.ID
	published, err := client.OnetimeProducts.Publish(ctx, pancake.PublishOnetimeProductParams{ID: productID})
	if err != nil {
		return "", fmt.Errorf("publish Waffo Pancake plan product: %w", err)
	}
	if published == nil || published.Product.ID != productID {
		return "", fmt.Errorf("Waffo Pancake returned invalid published product")
	}
	return productID, nil
}

// CreatePrimaryProduct mints (and publishes) the wallet-top-up
// OnetimeProduct under storeID. Per-checkout price overrides via PriceSnapshot
// are what make the "1.00" seed price irrelevant at runtime.
func (s *Service) CreatePrimaryProduct(ctx context.Context, merchantID, privateKey, storeID, returnURL string) (string, error) {
	return s.CreateProductForPlan(ctx, merchantID, privateKey, storeID, defaultWaffoPancakeProductName, "1.00", returnURL)
}

// PairResult is the response of CreatePrimaryPair.
// When OrphanStore is true the store was created but the product wasn't,
// so the caller can surface a partial-failure message with StoreID.
type PairResult struct {
	StoreID     string
	StoreName   string
	ProductID   string
	ProductName string
	OrphanStore bool
}

// CreatePrimaryPair mints and publishes a Store + OnetimeProduct pair — the canonical "+ Create" entry point. Nothing is persisted
// to settings; the operator's final Save commits the chosen IDs.
func (s *Service) CreatePrimaryPair(ctx context.Context, merchantID, privateKey, returnURL string) (*PairResult, error) {
	storeID, err := s.CreatePrimaryStore(ctx, merchantID, privateKey)
	if err != nil {
		return nil, err
	}
	productID, err := s.CreatePrimaryProduct(ctx, merchantID, privateKey, storeID, returnURL)
	if err != nil {
		return &PairResult{
			StoreID:     storeID,
			StoreName:   defaultWaffoPancakeStoreName,
			OrphanStore: true,
		}, fmt.Errorf("store created at %s but product creation failed: %w", storeID, err)
	}
	return &PairResult{
		StoreID:     storeID,
		StoreName:   defaultWaffoPancakeStoreName,
		ProductID:   productID,
		ProductName: defaultWaffoPancakeProductName,
	}, nil
}

// Save persists the operator-controlled fields atomically
// at the end of the configuration flow via the injected options manager (single
// DB transaction). A blank privateKey is treated as "keep current"
// (Stripe-style API-secret UX) and is omitted from the bulk payload.
func (s *Service) Save(ctx context.Context, merchantID, privateKey, returnURL, storeID, productID string) error {
	merchantID = strings.TrimSpace(merchantID)
	storeID = strings.TrimSpace(storeID)
	productID = strings.TrimSpace(productID)
	if merchantID == "" || storeID == "" || productID == "" {
		return fmt.Errorf("merchant id, store id, and product id are required to save")
	}
	values := map[string]string{
		"WaffoPancakeMerchantID": merchantID,
		"WaffoPancakeReturnURL":  strings.TrimSpace(returnURL),
		"WaffoPancakeStoreID":    storeID,
		"WaffoPancakeProductID":  productID,
	}
	if pk := strings.TrimSpace(privateKey); pk != "" {
		values["WaffoPancakePrivateKey"] = pk
	}
	if err := s.deps.SaveOptions(ctx, values); err != nil {
		return fmt.Errorf("persist Waffo Pancake config: %w", err)
	}
	return nil
}

type CatalogProduct struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// CatalogStore nests its OnetimeProducts so the UI can render a
// dependent store→product select without a second round-trip.
type CatalogStore struct {
	ID              string           `json:"id"`
	Name            string           `json:"name"`
	Status          string           `json:"status"`
	ProdEnabled     bool             `json:"prodEnabled"`
	OnetimeProducts []CatalogProduct `json:"onetimeProducts"`
}

type Catalog struct {
	Stores []CatalogStore `json:"stores"`
}

// Catalog queries Pancake's GraphQL `stores` for the
// merchant's stores + onetime products. A successful call also proves
// the supplied credentials authenticate (doubles as a credential probe).
func (s *Service) Catalog(ctx context.Context, merchantID, privateKey string) (*Catalog, error) {
	client, err := s.client(merchantID, privateKey)
	if err != nil {
		return nil, err
	}

	type queryShape struct {
		Stores []CatalogStore `json:"stores"`
	}
	// `limit: 100` because the API returns a single store when limit is
	// omitted, even for multi-store merchants. Bump to paginated fetches
	// (via `offset`) if real catalogs ever cross the cap.
	resp, err := pancake.GraphQLQuery[queryShape](ctx, client, pancake.GraphQLParams{
		Query: `query {
			stores(limit: 100) {
				id
				name
				status
				prodEnabled
				onetimeProducts {
					id
					name
					status
				}
			}
		}`,
	})
	if err != nil {
		return nil, fmt.Errorf("query Waffo Pancake catalog: %w", err)
	}
	if len(resp.Errors) > 0 {
		return nil, fmt.Errorf("waffo pancake catalog query returned %d errors: %s",
			len(resp.Errors), resp.Errors[0].Message)
	}
	// Drop non-active products. Operators should only see items they can
	// actually bind without later hitting "product unavailable" at checkout.
	stores := resp.Data.Stores
	for i := range stores {
		active := stores[i].OnetimeProducts[:0]
		for _, p := range stores[i].OnetimeProducts {
			if strings.EqualFold(strings.TrimSpace(p.Status), "active") {
				active = append(active, p)
			}
		}
		stores[i].OnetimeProducts = active
	}
	return &Catalog{Stores: stores}, nil
}
