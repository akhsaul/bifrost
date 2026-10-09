package datasheet

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

// RebuildFromURL deletes every pricing and model parameter row in the
// database and re-seeds both tables from the configured datasheets.
//
// Safety ordering: both datasheets are fetched and validated BEFORE anything
// is deleted. The delete + seed then runs inside a single transaction, so a
// seeding failure rolls back to the pre-rebuild rows instead of leaving
// empty tables behind.
//
// Pricing overrides (governance_pricing_overrides) are never touched — they
// are reloaded into memory by the caller after the rebuild.
func (s *Store) RebuildFromURL(ctx context.Context) error {
	if s.configStore == nil {
		return fmt.Errorf("cannot rebuild pricing catalog without a config store")
	}

	var pricingData map[string]Entry
	var paramsData map[string]json.RawMessage
	var pricingErr, paramsErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		pricingData, pricingErr = withRetries(ctx, urlFetchMaxRetries, urlFetchMaxBackoff, func() (map[string]Entry, error) {
			return s.loadPricingFromURL(ctx)
		})
	}()
	go func() {
		defer wg.Done()
		paramsData, paramsErr = withRetries(ctx, urlFetchMaxRetries, urlFetchMaxBackoff, func() (map[string]json.RawMessage, error) {
			return s.loadModelParametersFromURL(ctx)
		})
	}()
	wg.Wait()
	if pricingErr != nil {
		return fmt.Errorf("failed to load pricing data from URL: %w", pricingErr)
	}
	if paramsErr != nil {
		return fmt.Errorf("failed to load model parameters from URL: %w", paramsErr)
	}
	if len(pricingData) == 0 {
		return fmt.Errorf("refusing to rebuild pricing catalog: pricing datasheet is empty")
	}
	if len(paramsData) == 0 {
		return fmt.Errorf("refusing to rebuild pricing catalog: model parameters datasheet is empty")
	}
	if usableModelParameters(paramsData) == 0 {
		return fmt.Errorf("refusing to rebuild pricing catalog: model parameters datasheet has no usable records")
	}

	// Dedup by (model, provider, mode) before the write: the batched upsert
	// issues multi-row ON CONFLICT statements, and PostgreSQL rejects a
	// statement whose VALUES list hits the same conflict target twice.
	pricingRecords := make([]configstoreTables.TableModelPricing, 0, len(pricingData))
	seen := make(map[string]struct{}, len(pricingData))
	for modelKey, entry := range pricingData {
		pricing := convertEntryToTablePricing(modelKey, entry)
		key := makeKey(pricing.Model, pricing.Provider, pricing.Mode)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		pricingRecords = append(pricingRecords, pricing)
	}
	paramRecords := make([]configstoreTables.TableModelParameters, 0, len(paramsData))
	for model, data := range paramsData {
		paramRecords = append(paramRecords, configstoreTables.TableModelParameters{
			Model: model,
			Data:  string(data),
		})
	}

	if err := s.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		if err := s.configStore.DeleteModelPrices(ctx, tx); err != nil {
			return fmt.Errorf("failed to delete pricing records: %w", err)
		}
		if err := s.configStore.DeleteModelParameters(ctx, tx); err != nil {
			return fmt.Errorf("failed to delete model parameters: %w", err)
		}
		if err := s.configStore.UpsertModelPricesBatch(ctx, pricingRecords, tx); err != nil {
			return fmt.Errorf("failed to seed pricing records: %w", err)
		}
		if err := s.configStore.UpsertModelParametersBatch(ctx, paramRecords, tx); err != nil {
			return fmt.Errorf("failed to seed model parameters: %w", err)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to rebuild pricing catalog in database: %w", err)
	}

	if err := s.LoadFromDB(ctx); err != nil {
		return fmt.Errorf("failed to reload pricing cache: %w", err)
	}
	s.applyModelParameters(paramsData)

	if s.logger != nil {
		s.logger.Info("successfully rebuilt pricing catalog with %d pricing and %d model parameters records", len(pricingData), len(paramsData))
	}
	return nil
}

// usableModelParameters counts records that decode into a non-empty
// capability set — the same bar applyModelParameters uses before swapping
// the in-memory indexes. A feed of valid-but-empty JSON objects must not
// pass validation.
func usableModelParameters(paramsData map[string]json.RawMessage) int {
	usable := 0
	for _, raw := range paramsData {
		var caps schemas.ModelCapabilities
		if err := json.Unmarshal(raw, &caps); err != nil {
			continue
		}
		if !IsEmptyModelCapabilities(&caps) {
			usable++
		}
	}
	return usable
}
