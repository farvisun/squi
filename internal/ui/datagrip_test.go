package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/farvisun/squi/internal/config"
	"github.com/farvisun/squi/internal/datagrip"
)

const dgID = "866f1af3-d5f9-4900-aaf4-0fb4fca55a55"

// dataGripFixture writes a JetBrains root whose DataGrip has one project
// with a Postgres data source and a query console.
func dataGripFixture(t *testing.T) (root, project string) {
	t.Helper()
	root, project = t.TempDir(), t.TempDir()
	files := map[string]string{
		filepath.Join(root, "DataGrip2026.2", "options", "recentProjects.xml"):       `<application><component name="RecentProjectsManager"><option name="additionalInfo"><map><entry key="` + project + `"/></map></option></component></application>`,
		filepath.Join(root, "DataGrip2026.2", "consoles", "db", dgID, "console.sql"): "-- check\nselect count(*) from orders;\n",
		filepath.Join(project, ".idea", "dataSources.xml"): `<project version="4"><component name="DataSourceManagerImpl">
  <data-source source="LOCAL" name="orders prod" uuid="` + dgID + `"><driver-ref>postgresql</driver-ref><jdbc-url>jdbc:postgresql://db:5432/orders</jdbc-url></data-source>
</component></project>`,
	}
	for p, s := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, project
}

func TestDataGripConsoles(t *testing.T) {
	root, project := dataGripFixture(t)
	m, tb, _ := newTestModel(t, config.Connection{ID: "c1", Name: "orders", Driver: "postgres",
		Origin: &config.Origin{Tool: datagrip.Tool, ID: dgID, Source: project}})
	m.dataGripRoot = root
	drain(m, m.consolePicker(tb))
	if len(m.modals) != 1 {
		t.Fatalf("modals = %d", len(m.modals))
	}
	if v := m.View(); !strings.Contains(v, "console · select count(*) from orders;") {
		t.Fatalf("picker:\n%s", v)
	}
	key(m, "enter")
	if len(m.tabs) != 2 || m.currentTab().label != "console" ||
		m.currentTab().editor.Value() != "-- check\nselect count(*) from orders;\n" {
		t.Fatalf("tabs %d, current %q: %q", len(m.tabs), m.currentTab().label, m.currentTab().editor.Value())
	}

	other, tb2, _ := newTestModel(t, config.Connection{ID: "c2", Name: "x", Driver: "postgres"})
	other.dataGripRoot = root
	drain(other, other.consolePicker(tb2))
	if len(other.modals) != 0 {
		t.Error("picker opened for a connection not from DataGrip")
	}
}

func TestDataGripSyncFromManager(t *testing.T) {
	root, _ := dataGripFixture(t)
	m, _, _ := newTestModel(t, config.Connection{ID: "c1", Name: "x", Driver: "postgres"})
	m.dataGripRoot = root
	m.openConnections()
	key(m, "ctrl+g")
	if len(m.modals) != 1 || !strings.Contains(m.View(), "1 new") {
		t.Fatalf("no preview:\n%s", m.View())
	}
	if conns, _ := m.store.LoadConnections(); len(conns) != 0 {
		t.Fatal("saved before confirming")
	}
	key(m, "y")
	conns, _ := m.store.LoadConnections()
	if len(conns) != 1 || conns[0].Name != "orders prod" || conns[0].Env != config.EnvProd || len(m.conns) != 1 {
		t.Fatalf("after import: %+v", conns)
	}
	if !strings.Contains(m.View(), "Imported from DataGrip: 1 new") {
		t.Error("no result shown")
	}
	key(m, "ctrl+g")
	if len(m.modals) != 0 || !strings.Contains(m.View(), "DataGrip is in sync") {
		t.Error("asked again although nothing changed")
	}
	m.dataGripRoot = t.TempDir()
	key(m, "ctrl+g")
	if !strings.Contains(m.View(), "no DataGrip data sources found") {
		t.Errorf("missing DataGrip not reported:\n%s", m.View())
	}
}
