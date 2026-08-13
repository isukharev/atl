package httpx

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestTransportResponsibilityOwnersStayClosed(t *testing.T) {
	expected := map[string][]string{
		"attempt.go": {
			"(*Client).classifyAttempt", "(*Client).classifyResult", "attemptResult",
		},
		"body.go": {
			"(*idleReader).Close", "(*idleReader).Read", "(*idleReader).watchdog",
			"BinBodyCap", "ReadCapped",
			"downloadIdleTimeout", "idleReader", "jsonBodyCap", "newIdleReader", "readBody", "readBudgetExhaustion",
			"readIdleResponseBody", "readResponseBody", "readResponseBodyWith",
		},
		"budget_stream.go": {
			"(*readBudgetStream).Close", "(*readBudgetStream).Read", "(*readBudgetStream).begin", "(*readBudgetStream).closeUnderlying", "(*readBudgetStream).finishUsage",
			"newDownloadStream", "newReadBudgetStream", "readBudgetStream",
		},
		"client.go": {
			"(*Client).Base", "(*Client).Do", "(*Client).DoStream", "(*Client).DoStreamSized", "(*Client).DoWithBodyLimit",
			"(*Client).GetJSON", "(*Client).GetJSONUseNumber", "(*Client).GetStream", "(*Client).ResolveGET", "(*Client).SendJSON",
			"(*Client).do", "Client", "New", "NewWithScheduler", "NewWithSchedulerTLS", "defaultTimeout", "newWithScheduler",
			"unmarshal", "userAgent",
		},
		"errors.go": {
			"(*APIError).Error", "(*APIError).HTTPStatus", "(*APIError).Unwrap", "(*TransportError).Error", "(*TransportError).Format",
			"(*TransportError).Is", "(*unclearedWriteError).DiagnosticWriteAttempted", "(*unclearedWriteError).DiagnosticWriteClearanceFailure",
			"(*unclearedWriteError).Error", "(*unclearedWriteError).Unwrap", "APIError", "TransportError", "classify",
			"errUnclearedWrite", "redactURLString", "sameHost", "traceURL", "transportError", "transportErrorCategory", "unclearedWriteError",
		},
		"options.go": {
			"(*Client).tracef", "Option", "WithGenericConflict", "WithRequiredWriteClearance", "WithTrace", "clientOptions",
			"resolveOptions", "traceRequestURL", "traceResponsePath", "writerIsNil",
		},
		"retry.go": {
			"backoff", "clampRetryAfter", "maxRetries", "maxRetryAfter", "replaySafe", "retryAfter", "sleep",
		},
		"scheduler.go": {
			"(*Scheduler).acquire", "(*Scheduler).deferFor", "(*scheduledBody).Close", "(*scheduledBody).Read", "(scheduledRoundTripper).RoundTrip",
			"NewScheduler", "Scheduler", "scheduleTransport", "scheduledBody", "scheduledRoundTripper", "transientRetryStatus",
		},
		"tls.go": {
			"(TLSOptions).configured", "(TLSOptions).transport", "QualifiedTLSOptions", "TLSOptions", "ValidateCABundle", "caBundleMaxSize", "dlHeaderTimeout", "exclusiveCertPool", "readCABundle", "transportWithCABundle", "transportWithCertPool",
		},
		"transport.go": {
			"(*Client).newRequest", "(*Client).newRequestReader", "(*Client).resolveURL", "(readBudgetTransport).RoundTrip", "readBudgetTransport",
		},
	}

	actual := map[string][]string{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, declaration := range file.Decls {
			switch value := declaration.(type) {
			case *ast.FuncDecl:
				name := value.Name.Name
				if value.Recv != nil && len(value.Recv.List) == 1 {
					name = "(" + receiverName(t, value.Recv.List[0].Type) + ")." + name
				}
				actual[entry.Name()] = append(actual[entry.Name()], name)
			case *ast.GenDecl:
				for _, spec := range value.Specs {
					switch spec := spec.(type) {
					case *ast.TypeSpec:
						actual[entry.Name()] = append(actual[entry.Name()], spec.Name.Name)
					case *ast.ValueSpec:
						for _, name := range spec.Names {
							actual[entry.Name()] = append(actual[entry.Name()], name.Name)
						}
					}
				}
			}
		}
	}
	for file := range actual {
		sort.Strings(actual[file])
	}
	for file := range expected {
		sort.Strings(expected[file])
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("httpx production declarations = %#v, want exactly %#v", actual, expected)
	}
}

func receiverName(t *testing.T, expression ast.Expr) string {
	t.Helper()
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.StarExpr:
		return "*" + receiverName(t, value.X)
	default:
		t.Fatalf("unexpected receiver expression %T", expression)
		return ""
	}
}
