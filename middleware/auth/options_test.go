// Copyright (c) 2021 rookie-ninja
//
// Use of this source code is governed by an Apache-style
// license that can be found in the LICENSE file.

package auth

import (
	"encoding/base64"
	"fmt"
	"github.com/rookie-ninja/rk-entry/v3/middleware"
	"github.com/stretchr/testify/assert"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOptionSet_BeforeCtx(t *testing.T) {
	set := NewOptionSet()

	// without request
	ctx := set.BeforeCtx(nil)
	assert.NotNil(t, ctx)

	// with http.Request
	req := httptest.NewRequest(http.MethodGet, "/ut-path", nil)
	req.Header.Set(rkm.HeaderAuthorization, "basic")
	req.Header.Set(rkm.HeaderApiKey, "apiKey")
	ctx = set.BeforeCtx(req)

	assert.Equal(t, "basic", ctx.Input.BasicAuthHeader)
	assert.Equal(t, "apiKey", ctx.Input.ApiKeyHeader)
	assert.Empty(t, ctx.Output.HeadersToReturn)
	assert.Nil(t, ctx.Output.ErrResp)
}

func TestToOptions(t *testing.T) {
	config := &BootConfig{
		Enabled: false,
		Ignore:  []string{},
		Basic:   []string{},
		ApiKey:  []string{},
	}

	// with disabled
	assert.Empty(t, ToOptions(config, "", ""))

	// with enabled
	config.Enabled = true
	assert.NotEmpty(t, ToOptions(config, "", ""))
}

func TestNewOptionSet(t *testing.T) {
	// without options
	set := NewOptionSet().(*optionSet)

	assert.NotEmpty(t, set.EntryName())
	assert.NotNil(t, set.basicAccounts)
	assert.NotNil(t, set.apiKey)
	assert.Empty(t, set.pathToIgnore)

	// with options
	set = NewOptionSet(
		WithEntryNameAndKind("ut-name", "ut-kind"),
		WithBasicAuth("ut-realm", "user:pass"),
		WithApiKeyAuth("ut-key"),
		WithPathToIgnore("ut-ignore")).(*optionSet)

	assert.NotEmpty(t, set.EntryName())
	assert.NotEmpty(t, set.EntryKind())
	assert.NotEmpty(t, set.basicRealm)
	assert.NotEmpty(t, set.basicAccounts)
	assert.NotEmpty(t, set.apiKey)
	assert.NotEmpty(t, set.pathToIgnore)
}

func TestOptionSet_isBasicAuthorized(t *testing.T) {
	// case 1: auth header is provided
	// case 1.1: invalid basic auth
	req := httptest.NewRequest(http.MethodGet, "/ut-path", nil)
	req.Header.Set(rkm.HeaderAuthorization, "invalid")

	set := NewOptionSet().(*optionSet)
	resp := set.isBasicAuthorized(set.BeforeCtx(req))
	assert.Contains(t, resp.Error(), http.StatusText(http.StatusUnauthorized))

	// case 1.2: not authorized
	req = httptest.NewRequest(http.MethodGet, "/ut-path", nil)
	req.Header.Set(rkm.HeaderAuthorization, "Basic invalid")

	set = NewOptionSet().(*optionSet)
	ctx := set.BeforeCtx(req)
	resp = set.isBasicAuthorized(ctx)
	assert.NotEmpty(t, ctx.Output.HeadersToReturn)
	assert.Contains(t, resp.Error(), http.StatusText(http.StatusUnauthorized))

	// case 1.3: authorized
	req = httptest.NewRequest(http.MethodGet, "/ut-path", nil)
	req.Header.Set(rkm.HeaderAuthorization, fmt.Sprintf("Basic %s", base64.StdEncoding.EncodeToString([]byte("user:pass"))))

	set = NewOptionSet(WithBasicAuth("", "user:pass")).(*optionSet)
	ctx = set.BeforeCtx(req)
	resp = set.isBasicAuthorized(ctx)
	assert.Nil(t, resp)

	// case 2: auth header missing
	req = httptest.NewRequest(http.MethodGet, "/ut-path", nil)

	set = NewOptionSet().(*optionSet)
	ctx = set.BeforeCtx(req)
	resp = set.isBasicAuthorized(ctx)
	assert.Contains(t, resp.Error(), http.StatusText(http.StatusUnauthorized))
}

func TestOptionSet_isApiKeyAuthorized(t *testing.T) {
	// case 1: auth header is provided
	// case 1.1: not authorized
	req := httptest.NewRequest(http.MethodGet, "/ut-path", nil)
	req.Header.Set(rkm.HeaderApiKey, "invalid")

	set := NewOptionSet().(*optionSet)
	ctx := set.BeforeCtx(req)
	resp := set.isApiKeyAuthorized(ctx)
	assert.Contains(t, resp.Error(), http.StatusText(http.StatusUnauthorized))

	// case 1.2: authorized
	req = httptest.NewRequest(http.MethodGet, "/ut-path", nil)
	req.Header.Set(rkm.HeaderApiKey, "key")

	set = NewOptionSet(WithApiKeyAuth("key")).(*optionSet)
	ctx = set.BeforeCtx(req)
	resp = set.isApiKeyAuthorized(ctx)
	assert.Nil(t, resp)

	// case 2: auth header missing
	req = httptest.NewRequest(http.MethodGet, "/ut-path", nil)

	set = NewOptionSet().(*optionSet)
	ctx = set.BeforeCtx(req)
	resp = set.isApiKeyAuthorized(ctx)
	assert.Contains(t, resp.Error(), http.StatusText(http.StatusUnauthorized))
}

func TestOptionSet_Before(t *testing.T) {
	// case 0: ignore path
	req := httptest.NewRequest(http.MethodGet, "/ut-path", nil)

	set := NewOptionSet(WithPathToIgnore("/ut-path"))
	ctx := set.BeforeCtx(req)
	set.Before(ctx)
	assert.Nil(t, ctx.Output.ErrResp)

	// case 1: basic auth passed
	req = httptest.NewRequest(http.MethodGet, "/ut-path", nil)
	req.Header.Set(rkm.HeaderAuthorization, fmt.Sprintf("Basic %s", base64.StdEncoding.EncodeToString([]byte("user:pass"))))

	set = NewOptionSet(WithBasicAuth("", "user:pass"))
	ctx = set.BeforeCtx(req)
	set.Before(ctx)
	assert.Nil(t, ctx.Output.ErrResp)

	// case 2: X-API-Key passed
	req = httptest.NewRequest(http.MethodGet, "/ut-path", nil)
	req.Header.Set(rkm.HeaderApiKey, "key")

	set = NewOptionSet(WithApiKeyAuth("key"))
	ctx = set.BeforeCtx(req)
	set.Before(ctx)
	assert.Nil(t, ctx.Output.ErrResp)

	// case 3: basic auth provided, then return code and response related to basic auth
	req = httptest.NewRequest(http.MethodGet, "/ut-path", nil)
	req.Header.Set(rkm.HeaderAuthorization, "Basic invalid")

	set = NewOptionSet(WithBasicAuth("", "user:pass"))
	ctx = set.BeforeCtx(req)
	set.Before(ctx)
	assert.NotNil(t, ctx.Output.ErrResp)
	assert.Contains(t, ctx.Output.ErrResp.Error(), http.StatusText(http.StatusUnauthorized))
	assert.NotEmpty(t, ctx.Output.HeadersToReturn)

	// case 4: X-API-Key provided, then return code and response related to X-API-Key
	req = httptest.NewRequest(http.MethodGet, "/ut-path", nil)
	req.Header.Set(rkm.HeaderApiKey, "invalid")

	set = NewOptionSet(WithApiKeyAuth("key"))
	ctx = set.BeforeCtx(req)
	set.Before(ctx)
	assert.NotNil(t, ctx.Output.ErrResp)
	assert.Contains(t, ctx.Output.ErrResp.Error(), http.StatusText(http.StatusUnauthorized))

	// case 5: no auth provided, return bellow code and response
	// case 5.1: basic auth needed
	req = httptest.NewRequest(http.MethodGet, "/ut-path", nil)

	set = NewOptionSet(WithBasicAuth("", "user:pass"))
	ctx = set.BeforeCtx(req)
	set.Before(ctx)
	assert.NotNil(t, ctx.Output.ErrResp)
	assert.Contains(t, ctx.Output.ErrResp.Error(), http.StatusText(http.StatusUnauthorized))
	assert.NotEmpty(t, ctx.Output.HeadersToReturn)

	// case 5.2: X-API-Key needed
	req = httptest.NewRequest(http.MethodGet, "/ut-path", nil)

	set = NewOptionSet(WithApiKeyAuth("key"))
	ctx = set.BeforeCtx(req)
	set.Before(ctx)
	assert.NotNil(t, ctx.Output.ErrResp)
	assert.Contains(t, ctx.Output.ErrResp.Error(), http.StatusText(http.StatusUnauthorized))
}

func TestNewOptionSetMock(t *testing.T) {
	mock := NewOptionSetMock(NewBeforeCtx())
	assert.NotEmpty(t, mock.EntryName())
	assert.NotEmpty(t, mock.EntryKind())
	assert.NotNil(t, mock.BeforeCtx(nil))
	mock.Before(nil)
}
