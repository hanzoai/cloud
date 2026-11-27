package manifest

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestEveryStoreOnHanzoSQLHasAnAppThatOpensIt(t *testing.T) {
	for subsystem, app := range SQLStores {
		if _, ok := Named(app); !ok {
			t.Errorf("%s is opened by %q, which is not an app", subsystem, app)
		}
	}
	// The owners are the apps SQLStores names, once each and sorted; the first three
	// ports are among them.
	var want []string
	for _, app := range SQLStores {
		if !slices.Contains(want, app) {
			want = append(want, app)
		}
	}
	slices.Sort(want)
	if got := SQLOwners(); !slices.Equal(got, want) {
		t.Errorf("SQLOwners = %v, want %v", got, want)
	}
	for _, app := range []string{"commerce", "flag", "treasury"} {
		if !slices.Contains(want, app) {
			t.Errorf("%s no longer opens a store on hanzo-sql", app)
		}
	}
}

func connsOf(t *testing.T, name string) string {
	t.Helper()
	a, ok := Named(name)
	if !ok {
		t.Fatalf("no app %q", name)
	}
	for _, kv := range a.Plugin().Env {
		if v, ok := strings.CutPrefix(kv, sqlConnsEnv+"="); ok {
			return v
		}
	}
	return ""
}

// The host's CLOUD_SQL_CONNS is the replica's. Its children are handed shares that sum
// to no more than it, where an unmodified environment gave each of them all of it.
//
// The budget is stated as a share of 16 per owner, whatever number of owners there are,
// because every owner is handed at least sqlShareFloor and so a fixed 50 stops covering
// the floor once there are more than twelve of them: a deployment that ports that many
// stores states its budget (LLM.md, the arithmetic).
func TestTheHostDividesTheReplicasBudgetAmongItsChildren(t *testing.T) {
	owners := len(SQLOwners())
	// A budget that holds the floor of every owner: below that, every child is still
	// handed the floor and the sum is more than the budget, which is the next test.
	budget := 4 * sqlShareFloor * owners
	t.Setenv(sqlDSNEnv, "postgres://u@db.example.com/x?sslmode=require")
	t.Setenv(sqlConnsEnv, strconv.Itoa(budget))

	sum := 0
	for _, owner := range SQLOwners() {
		share, err := strconv.Atoi(connsOf(t, owner))
		if err != nil {
			t.Fatalf("%s was handed %q", owner, connsOf(t, owner))
		}
		sum += share
	}
	if sum > budget {
		t.Fatalf("the children that open stores were handed %d connections of a replica's %d", sum, budget)
	}
	if share := strconv.Itoa(budget / owners); connsOf(t, "flag") != share {
		t.Errorf("flag was handed %q of %d among %d, want %s", connsOf(t, "flag"), budget, owners, share)
	}
	// An app that opens no store is given one connection: the registry reads it may
	// make, and what a mistake costs.
	// Any app that is not an owner will do, and the first is taken so the test does not
	// name one that a later port makes an owner.
	var bareApp App
	for _, a := range Apps {
		if !slices.Contains(SQLOwners(), a.Name) {
			bareApp = a
			break
		}
	}
	if bareApp.Name == "" {
		bareApp = App{Name: "stateless-leaf"}
	}
	var bareGot string
	for _, kv := range bareApp.Plugin().Env {
		if v, ok := strings.CutPrefix(kv, sqlConnsEnv+"="); ok {
			bareGot = v
		}
	}
	if bareGot != "1" {
		t.Errorf("%s, an app with no store on hanzo-sql, was handed %q, want 1", bareApp.Name, bareGot)
	}

	// A small budget does not starve a child below what lets it open a store.
	t.Setenv(sqlConnsEnv, "2")
	if got := connsOf(t, "flag"); got != strconv.Itoa(sqlShareFloor) {
		t.Errorf("with 6 among %d, flag was handed %q, want the floor %d", owners, got, sqlShareFloor)
	}

	// Unset, the replica's budget is the default, and it is still divided.
	t.Setenv(sqlConnsEnv, "")
	if share := strconv.Itoa(max(sqlShareFloor, sqlConnsDefault/owners)); connsOf(t, "treasury") != share {
		t.Errorf("with no budget stated, treasury was handed %q, want %s of the default %d", connsOf(t, "treasury"), share, sqlConnsDefault)
	}
}

func TestNothingIsHandedDownWhenThereIsNoDSN(t *testing.T) {
	t.Setenv(sqlDSNEnv, "")
	t.Setenv(sqlConnsEnv, "50")
	if got := connsOf(t, "flag"); got != "" {
		t.Fatalf("a deployment on SQLite handed flag %q", got)
	}
}

// Every plugin binary embeds a tasks engine named for its app (Serve →
// installDurableIngest), not only the apps that call cloud.EmbeddedTasks, so every
// app that runs as its own process declares "tasks-<app>". Undeclared, its engine
// keeps shard files on the pod's disk (durableDatabase), which blocks a second
// replica and loses schedules with the volume.
func TestEveryAppThatRunsAsAProcessDeclaresItsEngine(t *testing.T) {
	for _, a := range Apps {
		if a.Coresident {
			continue
		}
		if got := SQLStores["tasks-"+a.Name]; got != a.Name {
			t.Errorf("%s runs an embedded tasks engine and SQLStores[tasks-%s] = %q", a.Name, a.Name, got)
		}
	}
}

// The production budget: CLOUD_SQL_CONNS=1260 divided among every owner. Each share is
// at least the floor, and the shares sum to no more than the budget.
func TestTheProductionBudgetCoversEveryOwner(t *testing.T) {
	const budget = 1260
	owners := len(SQLOwners())
	if sqlShareFloor*owners > budget {
		t.Fatalf("%d owners at the floor of %d need %d, past the budget %d", owners, sqlShareFloor, sqlShareFloor*owners, budget)
	}
	t.Setenv(sqlDSNEnv, "postgres://u@db.example.com/x?sslmode=require")
	t.Setenv(sqlConnsEnv, strconv.Itoa(budget))
	sum := 0
	for _, o := range SQLOwners() {
		n, _ := strconv.Atoi(connsOf(t, o))
		sum += n
	}
	if sum > budget {
		t.Fatalf("owners were handed %d of %d", sum, budget)
	}
	t.Logf("owners=%d share=%d handed=%d of %d", owners, budget/owners, sum, budget)
}
