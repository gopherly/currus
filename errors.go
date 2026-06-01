// Copyright 2026 The Gopherly Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package currus

import "errors"

// Sentinel errors returned by all Engine implementations.
// Use [errors.Is] to compare; all errors may be wrapped.
var (
	// ErrNotFound is returned when the requested resource does not exist.
	ErrNotFound = errors.New("not found")

	// ErrAlreadyExists is returned when a resource already exists and the
	// operation requires uniqueness.
	ErrAlreadyExists = errors.New("already exists")

	// ErrConflict is returned when an operation conflicts with the current
	// state of a resource.
	ErrConflict = errors.New("conflict")

	// ErrNotImplemented is returned by a driver that has not yet implemented
	// a particular method of a capability interface.
	ErrNotImplemented = errors.New("not implemented")

	// ErrUnsupported is returned when the underlying engine does not support
	// the requested operation at all.
	ErrUnsupported = errors.New("unsupported")

	// ErrNoEngine is returned by New when no reachable engine is found.
	ErrNoEngine = errors.New("no reachable container engine found")

	// ErrInvalidSpec is returned when a ContainerSpec or option field is
	// malformed or missing a required value (e.g. an empty Image field).
	ErrInvalidSpec = errors.New("invalid container spec")
)
