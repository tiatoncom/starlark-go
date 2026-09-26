// Copyright 2020 The Bazel Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package starlarkjson is an alias for github.com/tiatoncom/starlark-go/lib/json to provide
// backwards compatibility
//
// Deprecated: use github.com/tiatoncom/starlark-go/lib/json instead
package starlarkjson // import "github.com/tiatoncom/starlark-go/starlarkjson"

import (
	"github.com/tiatoncom/starlark-go/lib/json"
)

// Module is an alias of json.Module for backwards import compatibility
var Module = json.Module
