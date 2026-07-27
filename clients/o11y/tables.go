// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package o11y

// The o11y read plane's table vocabulary, in ONE place. Every direct-SQL read in
// this package names a table through these constants, so a schema move is a single
// edit and the tripwire tests (tables_test.go) can assert the whole read surface at
// once. The write side is the collector; the two must be bumped together.
//
// One generation, no version suffixes: a database names the signal, a table names
// the rows, and `distributed_` is topology (a Distributed engine over the local
// table), not a version.
const (
	// spanTable is the span plane — every trace row cloud reads.
	spanTable = "o11y_traces.distributed_spans"

	// logTable is the log-record plane — the raw stdout stream per workload.
	logTable = "o11y_logs.distributed_records"
)

// Span columns. Service name and HTTP route are materialized resource/attribute
// columns whose identifiers contain `$$`, so each is backtick-quoted at every use.
const (
	spanService = "`resource_string_service$$name`"
	spanRoute   = "`attribute_string_http$$route`"
)
