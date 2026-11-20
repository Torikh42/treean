package migrations_test

import (
	"bytes"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/torikh42/treean/migrations"
	"golang.org/x/crypto/bcrypt"
)

var (
	utf8BOM             = []byte{0xEF, 0xBB, 0xBF}
	bcryptHashPattern   = regexp.MustCompile(`\$2[aby]\$\d{2}\$[./A-Za-z0-9]{53}`)
	subtotalCheckRegexp = regexp.MustCompile(`CHECK \(subtotal_idr = unit_price_idr \* quantity(?:::bigint)?\)`)
	fromUsersRegexp     = regexp.MustCompile(`(?i)\bFROM\s+users\b`)
	organizerEmailRegex = regexp.MustCompile(`(?i)email\s*=\s*'organizer@event\.local'`)
	fromEventsRegexp    = regexp.MustCompile(`(?i)\bFROM\s+events\b`)
)

func normalizeStatement(raw string) string {
	return strings.Join(strings.Fields(raw), " ")
}

func scanSingleQuoted(src string, start int) int {
	for i := start + 1; i < len(src); i++ {
		if src[i] != '\'' {
			continue
		}
		if i+1 < len(src) && src[i+1] == '\'' {
			i++
			continue
		}
		return i + 1
	}
	return len(src)
}

func skipToLineEnd(src string, start int) int {
	idx := strings.IndexByte(src[start:], '\n')
	if idx < 0 {
		return len(src) - 1
	}
	return start + idx
}

func skipBlockComment(src string, start int) int {
	idx := strings.Index(src[start:], "*/")
	if idx < 0 {
		return len(src) - 1
	}
	return start + idx + 1
}

func stripSQLComments(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	for i := 0; i < len(src); i++ {
		if src[i] == '\'' {
			end := scanSingleQuoted(src, i)
			b.WriteString(src[i:end])
			i = end - 1
			continue
		}
		if strings.HasPrefix(src[i:], "--") {
			i = skipToLineEnd(src, i+2)
			b.WriteByte('\n')
			continue
		}
		if strings.HasPrefix(src[i:], "/*") {
			i = skipBlockComment(src, i+2)
			b.WriteByte(' ')
			continue
		}
		b.WriteByte(src[i])
	}
	return b.String()
}

func splitSQLStatements(raw []byte) []string {
	cleaned := stripSQLComments(string(raw))
	var stmts []string
	start := 0
	for i := 0; i < len(cleaned); i++ {
		if cleaned[i] == '\'' {
			i = scanSingleQuoted(cleaned, i) - 1
			continue
		}
		if cleaned[i] != ';' {
			continue
		}
		if stmt := normalizeStatement(cleaned[start:i]); stmt != "" {
			stmts = append(stmts, stmt)
		}
		start = i + 1
	}
	if stmt := normalizeStatement(cleaned[start:]); stmt != "" {
		stmts = append(stmts, stmt)
	}
	return stmts
}

func extractIdentifierAfter(stmt, prefix string) string {
	rest := strings.TrimSpace(stmt[len(prefix):])
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return ""
	}
	name, _, _ := strings.Cut(fields[0], "(")
	return strings.TrimSpace(name)
}

func readEmbeddedSQL(t *testing.T, name string) []byte {
	t.Helper()
	data, err := fs.ReadFile(migrations.Files, name)
	if err != nil {
		t.Fatalf("fs.ReadFile(migrations.Files, %q) error = %v", name, err)
	}
	if len(data) == 0 {
		t.Fatalf("embedded SQL file %q is empty", name)
	}
	if bytes.HasPrefix(data, utf8BOM) {
		t.Fatalf("embedded SQL file %q contains a UTF-8 BOM", name)
	}
	return data
}

func parseSchemaStatements(t *testing.T, stmts []string) (tables, indexes map[string]string, sequences []string) {
	t.Helper()
	tables = make(map[string]string)
	indexes = make(map[string]string)

	for _, stmt := range stmts {
		upper := strings.ToUpper(stmt)
		if strings.Contains(upper, "CREATE EXTENSION") {
			t.Fatalf("unexpected CREATE EXTENSION statement in PostgreSQL 17 schema: %s", stmt)
		}

		switch {
		case strings.HasPrefix(upper, "CREATE TABLE "):
			const prefix = "CREATE TABLE IF NOT EXISTS "
			if !strings.HasPrefix(upper, prefix) {
				t.Fatalf("CREATE TABLE must include IF NOT EXISTS: %s", stmt)
			}
			tables[extractIdentifierAfter(stmt, prefix)] = stmt
		case strings.HasPrefix(upper, "CREATE UNIQUE INDEX "):
			const prefix = "CREATE UNIQUE INDEX IF NOT EXISTS "
			if !strings.HasPrefix(upper, prefix) {
				t.Fatalf("CREATE UNIQUE INDEX must include IF NOT EXISTS: %s", stmt)
			}
			indexes[extractIdentifierAfter(stmt, prefix)] = stmt
		case strings.HasPrefix(upper, "CREATE INDEX "):
			const prefix = "CREATE INDEX IF NOT EXISTS "
			if !strings.HasPrefix(upper, prefix) {
				t.Fatalf("CREATE INDEX must include IF NOT EXISTS: %s", stmt)
			}
			indexes[extractIdentifierAfter(stmt, prefix)] = stmt
		case strings.HasPrefix(upper, "CREATE SEQUENCE "):
			const prefix = "CREATE SEQUENCE IF NOT EXISTS "
			if !strings.HasPrefix(upper, prefix) {
				t.Fatalf("CREATE SEQUENCE must include IF NOT EXISTS: %s", stmt)
			}
			sequences = append(sequences, extractIdentifierAfter(stmt, prefix))
		}
	}
	return tables, indexes, sequences
}

func TestSplitSQLStatements_StripsCommentsAndRespectsQuotes(t *testing.T) {
	t.Parallel()

	sample := []byte(`
		-- leading comment with ; semicolon
		CREATE TABLE IF NOT EXISTS demo (
			note TEXT NOT NULL DEFAULT '-- not a comment /* keep */;'
		);
		/* multi-line
		   block comment ; */
		INSERT INTO demo (note) VALUES ('ok') ON CONFLICT DO NOTHING;
	`)

	got := splitSQLStatements(sample)
	if len(got) != 2 {
		t.Fatalf("splitSQLStatements count = %d, want 2: %#v", len(got), got)
	}
	if !strings.Contains(got[0], "'-- not a comment /* keep */;'") {
		t.Fatalf("statement 0 lost quoted literal content: %s", got[0])
	}
	if !strings.HasPrefix(got[1], "INSERT INTO demo") {
		t.Fatalf("statement 1 = %q, want INSERT INTO demo ...", got[1])
	}
}

func TestFiles_OrderedSQLAssets(t *testing.T) {
	t.Parallel()

	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		t.Fatalf("fs.ReadDir(migrations.Files, \".\") error = %v", err)
	}

	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatalf("unexpected subdirectory %q in migrations.Files", entry.Name())
		}
		got = append(got, entry.Name())
	}

	want := []string{"001_init_schema.sql", "002_seed_demo.sql", "003_order_idempotency.sql"}
	if !slices.Equal(got, want) {
		t.Fatalf("embedded migration files = %v, want %v", got, want)
	}

	for _, name := range want {
		_ = readEmbeddedSQL(t, name)
	}
}

func TestInitSchemaSQL_ContractsAndIndexes(t *testing.T) {
	t.Parallel()

	stmts := splitSQLStatements(readEmbeddedSQL(t, "001_init_schema.sql"))
	tables, indexes, sequences := parseSchemaStatements(t, stmts)

	if !slices.Contains(sequences, "invoice_seq") {
		t.Fatalf("missing invoice_seq sequence in %v", sequences)
	}

	expectedTables := []string{
		"users",
		"events",
		"ticket_types",
		"orders",
		"order_items",
		"order_logs",
		"tickets",
		"payment_webhooks",
	}
	for _, table := range expectedTables {
		if _, ok := tables[table]; !ok {
			t.Fatalf("missing required table %q", table)
		}
	}

	tableChecks := []struct {
		name      string
		table     string
		clauses   []string
		forbidden []string
		regexpReq *regexp.Regexp
	}{
		{
			name:  "users constraints",
			table: "users",
			clauses: []string{
				"email TEXT NOT NULL UNIQUE",
				"CHECK (role IN ('ORGANIZER', 'BUYER'))",
			},
		},
		{
			name:  "events foreign key",
			table: "events",
			clauses: []string{
				"organizer_id UUID NOT NULL REFERENCES users(id)",
			},
		},
		{
			name:  "ticket_types quota and uniqueness",
			table: "ticket_types",
			clauses: []string{
				"CHECK (price_idr >= 0)",
				"CHECK (total_quota > 0)",
				"CHECK (remaining_quota >= 0 AND remaining_quota <= total_quota)",
				"UNIQUE (event_id, name)",
			},
		},
		{
			name:  "orders ticket cap and status enum",
			table: "orders",
			clauses: []string{
				"total_tickets INTEGER NOT NULL CHECK (total_tickets BETWEEN 1 AND 4)",
				"CHECK (total_amount_idr >= 0)",
				"CHECK (status IN ('WAITING_PAYMENT', 'PAID', 'CANCELLED', 'EXPIRED'))",
			},
		},
		{
			name:  "order_items quantity, subtotal integrity, and uniqueness",
			table: "order_items",
			clauses: []string{
				"CHECK (quantity > 0)",
				"UNIQUE (order_id, ticket_type_id)",
			},
			regexpReq: subtotalCheckRegexp,
		},
		{
			name:  "order_logs actor_type and status checks",
			table: "order_logs",
			clauses: []string{
				"CHECK (actor_type IN ('buyer', 'webhook', 'scheduler', 'lazy-check'))",
				"CHECK (from_status IS NULL OR from_status IN ('WAITING_PAYMENT', 'PAID', 'CANCELLED', 'EXPIRED'))",
				"CHECK (to_status IN ('WAITING_PAYMENT', 'PAID', 'CANCELLED', 'EXPIRED'))",
			},
		},
		{
			name:  "tickets uniqueness and seat sequence",
			table: "tickets",
			clauses: []string{
				"ticket_code TEXT NOT NULL UNIQUE",
				"CHECK (seat_sequence > 0)",
				"UNIQUE (order_item_id, seat_sequence)",
			},
		},
		{
			name:  "payment_webhooks decouples order FK for audit durability",
			table: "payment_webhooks",
			clauses: []string{
				"raw_order_id TEXT NOT NULL",
				"parsed_order_id UUID NULL",
			},
			forbidden: []string{
				"REFERENCES orders",
			},
		},
	}

	for _, tc := range tableChecks {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stmt := tables[tc.table]
			for _, clause := range tc.clauses {
				if !strings.Contains(stmt, clause) {
					t.Fatalf("table %q missing clause %q in %s", tc.table, clause, stmt)
				}
			}
			for _, bad := range tc.forbidden {
				if strings.Contains(stmt, bad) {
					t.Fatalf("table %q must not contain %q in %s", tc.table, bad, stmt)
				}
			}
			if tc.regexpReq != nil && !tc.regexpReq.MatchString(stmt) {
				t.Fatalf("table %q did not match %s in %s", tc.table, tc.regexpReq.String(), stmt)
			}
		})
	}

	indexChecks := []struct {
		name     string
		index    string
		expected string
	}{
		{
			name:     "ticket_types case-insensitive unique tier index",
			index:    "uidx_ticket_types_event_lower_name",
			expected: "CREATE UNIQUE INDEX IF NOT EXISTS uidx_ticket_types_event_lower_name ON ticket_types (event_id, LOWER(name))",
		},
		{
			name:     "events chronological listing index",
			index:    "idx_events_event_date_created",
			expected: "CREATE INDEX IF NOT EXISTS idx_events_event_date_created ON events (event_date ASC, created_at ASC)",
		},
		{
			name:     "orders partial covering buyer allowance index",
			index:    "idx_orders_buyer_event_status",
			expected: "CREATE INDEX IF NOT EXISTS idx_orders_buyer_event_status ON orders (event_id, buyer_id) INCLUDE (total_tickets) WHERE status IN ('WAITING_PAYMENT', 'PAID')",
		},
		{
			name:     "orders buyer created history index",
			index:    "idx_orders_buyer_created",
			expected: "CREATE INDEX IF NOT EXISTS idx_orders_buyer_created ON orders (buyer_id, created_at DESC)",
		},
		{
			name:     "orders partial active expiry index",
			index:    "idx_orders_active_expiry",
			expected: "CREATE INDEX IF NOT EXISTS idx_orders_active_expiry ON orders (event_id, expires_at) WHERE status = 'WAITING_PAYMENT'",
		},
		{
			name:     "orders partial global expiry index",
			index:    "idx_orders_global_expiry",
			expected: "CREATE INDEX IF NOT EXISTS idx_orders_global_expiry ON orders (expires_at) WHERE status = 'WAITING_PAYMENT'",
		},
		{
			name:     "order_logs order_id index",
			index:    "idx_order_logs_order_id",
			expected: "CREATE INDEX IF NOT EXISTS idx_order_logs_order_id ON order_logs (order_id, created_at)",
		},
		{
			name:     "tickets order_id index",
			index:    "idx_tickets_order_id",
			expected: "CREATE INDEX IF NOT EXISTS idx_tickets_order_id ON tickets (order_id)",
		},
		{
			name:     "tickets owner_id index",
			index:    "idx_tickets_owner_id",
			expected: "CREATE INDEX IF NOT EXISTS idx_tickets_owner_id ON tickets (owner_id)",
		},
		{
			name:     "payment_webhooks tx_id index",
			index:    "idx_payment_webhooks_tx_id",
			expected: "CREATE INDEX IF NOT EXISTS idx_payment_webhooks_tx_id ON payment_webhooks (transaction_id)",
		},
		{
			name:     "payment_webhooks partial parsed_order_id history index",
			index:    "idx_payment_webhooks_parsed_order_id",
			expected: "CREATE INDEX IF NOT EXISTS idx_payment_webhooks_parsed_order_id ON payment_webhooks (parsed_order_id, received_at DESC) WHERE parsed_order_id IS NOT NULL",
		},
	}

	for _, tc := range indexChecks {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := indexes[tc.index]
			if !ok {
				t.Fatalf("missing required index %q", tc.index)
			}
			if got != tc.expected {
				t.Fatalf("index %q = %q, want %q", tc.index, got, tc.expected)
			}
		})
	}
}

func parseSeedInserts(t *testing.T, raw []byte) map[string]string {
	t.Helper()

	stmts := splitSQLStatements(raw)
	if len(stmts) != 3 {
		t.Fatalf("002_seed_demo.sql statements = %d, want 3", len(stmts))
	}

	inserts := make(map[string]string, len(stmts))
	for _, stmt := range stmts {
		upper := strings.ToUpper(stmt)
		const prefix = "INSERT INTO "
		if !strings.HasPrefix(upper, prefix) {
			t.Fatalf("unexpected non-INSERT seed statement: %s", stmt)
		}
		if !strings.Contains(upper, "ON CONFLICT DO NOTHING") {
			t.Fatalf("seed INSERT statement must include ON CONFLICT DO NOTHING: %s", stmt)
		}
		inserts[extractIdentifierAfter(stmt, prefix)] = stmt
	}
	return inserts
}

func verifySeedBcryptHashes(t *testing.T, raw []byte) {
	t.Helper()

	hashes := bcryptHashPattern.FindAllString(string(raw), -1)
	if len(hashes) != 3 {
		t.Fatalf("found %d bcrypt hashes in 002_seed_demo.sql, want 3", len(hashes))
	}

	uniqueHashes := make(map[string]struct{}, len(hashes))
	for i, hash := range hashes {
		cost, err := bcrypt.Cost([]byte(hash))
		if err != nil {
			t.Fatalf("hash[%d] bcrypt.Cost(%q) error = %v", i, hash, err)
		}
		if cost != bcrypt.DefaultCost {
			t.Fatalf("hash[%d] cost = %d, want %d", i, cost, bcrypt.DefaultCost)
		}
		uniqueHashes[hash] = struct{}{}
	}

	for hash := range uniqueHashes {
		if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("password123")); err != nil {
			t.Fatalf("unique hash %q does not verify \"password123\": %v", hash, err)
		}
		if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("wrong-password")); err == nil {
			t.Fatalf("unique hash %q unexpectedly verified wrong password", hash)
		}
	}
}

func assertContainsAll(t *testing.T, label, stmt string, wants []string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(stmt, want) {
			t.Fatalf("%s missing %s", label, want)
		}
	}
}

func TestSeedDemoSQL_BcryptAndFixtures(t *testing.T) {
	t.Parallel()

	raw := readEmbeddedSQL(t, "002_seed_demo.sql")
	inserts := parseSeedInserts(t, raw)
	verifySeedBcryptHashes(t, raw)

	t.Run("users seed accounts", func(t *testing.T) {
		t.Parallel()
		stmt, ok := inserts["users"]
		if !ok {
			t.Fatal("missing INSERT INTO users statement")
		}
		assertContainsAll(t, "users insert", stmt, []string{"'organizer@event.local'", "'buyer1@event.local'", "'buyer2@event.local'", "'ORGANIZER'", "'BUYER'"})
	})

	t.Run("events seed resolves organizer_id via users", func(t *testing.T) {
		t.Parallel()
		stmt, ok := inserts["events"]
		if !ok {
			t.Fatal("missing INSERT INTO events statement")
		}
		if !strings.Contains(stmt, "'Tech Flash Concert 2026'") {
			t.Fatalf("events insert missing title in %s", stmt)
		}
		if !fromUsersRegexp.MatchString(stmt) || !organizerEmailRegex.MatchString(stmt) {
			t.Fatalf("events insert must resolve organizer_id from users WHERE email = 'organizer@event.local': %s", stmt)
		}
		if strings.Contains(stmt, "11111111-1111-1111-1111-111111111111") {
			t.Fatalf("events insert must not hardcode organizer UUID literal: %s", stmt)
		}
	})

	t.Run("ticket_types seed joins events", func(t *testing.T) {
		t.Parallel()
		stmt, ok := inserts["ticket_types"]
		if !ok {
			t.Fatal("missing INSERT INTO ticket_types statement")
		}
		if !fromEventsRegexp.MatchString(stmt) {
			t.Fatalf("ticket_types insert must join events to prevent FK error: %s", stmt)
		}
		assertContainsAll(t, "ticket_types insert", stmt, []string{"'Reguler'", "150000", "80", "'VIP'", "350000", "20"})
	})
}

func TestOrderIdempotencyMigrationSQL(t *testing.T) {
	t.Parallel()

	raw := readEmbeddedSQL(t, "003_order_idempotency.sql")
	stmts := splitSQLStatements(raw)
	_, indexes, _ := parseSchemaStatements(t, stmts)

	joined := strings.Join(stmts, "\n")
	if !strings.Contains(joined, "ADD COLUMN IF NOT EXISTS idempotency_key TEXT NULL") {
		t.Fatalf("missing idempotency_key column DDL: %s", joined)
	}
	idx, ok := indexes["uidx_orders_buyer_idempotency_key"]
	if !ok {
		t.Fatalf("missing uidx_orders_buyer_idempotency_key unique index in %v", indexes)
	}
	assertContainsAll(t, "idempotency unique index", idx, []string{"orders (buyer_id, idempotency_key)", "WHERE idempotency_key IS NOT NULL"})
}
