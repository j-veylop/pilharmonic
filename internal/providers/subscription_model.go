package providers

import (
	"context"
	"errors"
	"fmt"

	"charm.land/fantasy"
)

type subscriptionModel struct{ fantasy.LanguageModel }

func subscriptionError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var providerError *fantasy.ProviderError
	if errors.As(err, &providerError) && providerError.StatusCode >= 400 && providerError.StatusCode <= 599 {
		return fmt.Errorf("ChatGPT subscription request failed (HTTP %d)", providerError.StatusCode)
	}
	return errors.New("ChatGPT subscription request failed; retry or use /login openai-subscription")
}

func (m subscriptionModel) Generate(ctx context.Context, call fantasy.Call) (*fantasy.Response, error) {
	response, err := m.LanguageModel.Generate(ctx, call)
	if err != nil {
		return nil, subscriptionError(ctx, err)
	}
	return response, nil
}

func (m subscriptionModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	stream, err := m.LanguageModel.Stream(ctx, call)
	if err != nil {
		return nil, subscriptionError(ctx, err)
	}
	return func(yield func(fantasy.StreamPart) bool) {
		for part := range stream {
			if part.Error != nil {
				part = fantasy.StreamPart{Type: fantasy.StreamPartTypeError, Error: subscriptionError(ctx, part.Error)}
			}
			if !yield(part) {
				return
			}
		}
	}, nil
}

func (m subscriptionModel) GenerateObject(ctx context.Context, call fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	response, err := m.LanguageModel.GenerateObject(ctx, call)
	if err != nil {
		return nil, subscriptionError(ctx, err)
	}
	return response, nil
}

func (m subscriptionModel) StreamObject(ctx context.Context, call fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	stream, err := m.LanguageModel.StreamObject(ctx, call)
	if err != nil {
		return nil, subscriptionError(ctx, err)
	}
	return func(yield func(fantasy.ObjectStreamPart) bool) {
		for part := range stream {
			if part.Error != nil {
				part = fantasy.ObjectStreamPart{Type: fantasy.ObjectStreamPartTypeError, Error: subscriptionError(ctx, part.Error)}
			}
			if !yield(part) {
				return
			}
		}
	}, nil
}
