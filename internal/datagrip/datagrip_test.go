package datagrip

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/farvisun/squi/internal/config"
)

const (
	idPG     = "11111111-1111-1111-1111-111111111111"
	idMy     = "22222222-2222-2222-2222-222222222222"
	idLite   = "33333333-3333-3333-3333-333333333333"
	idOracle = "44444444-4444-4444-4444-444444444444"
	idMSSQL  = "55555555-5555-5555-5555-555555555555"
)

// fixture writes a JetBrains root with two DataGrip versions and a project
// shaped like DataGrip 2026.2 writes it.
func fixture(t *testing.T) (root, project string) {
	t.Helper()
	root = t.TempDir()
	project = filepath.Join(t.TempDir(), "dg")
	cfg := filepath.Join(root, "DataGrip2026.2")
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "DataGrip2025.3", "consoles", "db", idPG, "console.sql"), "select 'old';")
	write(filepath.Join(root, "WebStorm2026.2", "options", "other.xml"), "<application/>")
	write(filepath.Join(cfg, "options", "recentProjects.xml"), `<application><component name="RecentProjectsManager">
  <option name="additionalInfo"><map>
    <entry key="`+project+`"><value/></entry>
    <entry key="$USER_HOME$/does-not-exist"><value/></entry>
  </map></option></component></application>`)
	write(filepath.Join(cfg, "options", "sshConfigs.xml"), `<application><component name="SshConfigs"><configs>
  <sshConfig authType="OPEN_SSH" host="bastion" id="ssh-1" port="22" customName="bastion" nameFormat="CUSTOM" username="ops" useOpenSSHConfig="true">
    <option name="customName" value="bastion" />
  </sshConfig>
  <sshConfig authType="KEY_PAIR" host="jump.example.com" id="ssh-2" port="2222" keyPath="$USER_HOME$/.ssh/prod" username="deploy" />
</configs></component></application>`)
	write(filepath.Join(project, ".idea", "dataSources.xml"), `<?xml version="1.0" encoding="UTF-8"?>
<project version="4">
  <component name="DataSourceManagerImpl" format="xml" multifile-model="true">
    <data-source source="LOCAL" name="orders" group="shop/eu" uuid="`+idPG+`">
      <driver-ref>postgresql</driver-ref>
      <synchronize>true</synchronize>
      <jdbc-driver>org.postgresql.Driver</jdbc-driver>
      <jdbc-url>jdbc:postgresql://db.internal:6432,db2.internal:6432/orders?sslmode=require&amp;ApplicationName=dg&amp;prepareThreshold=0&amp;weird=1</jdbc-url>
      <working-dir>$ProjectFileDir$</working-dir>
    </data-source>
    <data-source source="LOCAL" name="shop prod" uuid="`+idMy+`">
      <driver-ref>mysql.8</driver-ref>
      <jdbc-url>jdbc:mysql://10.0.0.5/shop?useSSL=false&amp;serverTimezone=UTC</jdbc-url>
      <read-only>true</read-only>
    </data-source>
    <data-source source="LOCAL" name="app" uuid="`+idLite+`">
      <driver-ref>sqlite.xerial</driver-ref>
      <jdbc-url>jdbc:sqlite:$PROJECT_DIR$/../data/app.db</jdbc-url>
    </data-source>
    <data-source source="LOCAL" name="erp" uuid="`+idOracle+`">
      <driver-ref>oracle</driver-ref>
      <jdbc-url>jdbc:oracle:thin:@erp:1521:ORCL</jdbc-url>
    </data-source>
    <data-source source="LOCAL" name="crm staging" uuid="`+idMSSQL+`">
      <driver-ref>sqlserver.ms</driver-ref>
      <jdbc-url>jdbc:sqlserver://crm\SQLEXPRESS:14330;databaseName=crm;encrypt=true;trustServerCertificate=false</jdbc-url>
    </data-source>
  </component>
</project>`)
	write(filepath.Join(project, ".idea", "dataSources.local.xml"), `<?xml version="1.0" encoding="UTF-8"?>
<project version="4">
  <component name="dataSourceStorageLocal" created-in="DB-262">
    <data-source name="orders" uuid="`+idPG+`">
      <database-info product="PostgreSQL" version="16"/>
      <secret-storage>master_key</secret-storage>
      <user-name>app</user-name>
      <ssh-properties><enabled>true</enabled><ssh-config-id>ssh-1</ssh-config-id></ssh-properties>
    </data-source>
    <data-source name="shop prod" uuid="`+idMy+`">
      <user-name>root</user-name>
      <ssh-properties><enabled>true</enabled><ssh-config-id>ssh-2</ssh-config-id></ssh-properties>
    </data-source>
    <data-source name="app" uuid="`+idLite+`">
      <auth-provider>no-auth</auth-provider>
    </data-source>
    <data-source name="crm staging" uuid="`+idMSSQL+`">
      <user-name>sa</user-name>
      <ssh-properties><enabled>true</enabled><ssh-config-id>gone</ssh-config-id></ssh-properties>
    </data-source>
  </component>
</project>`)
	write(filepath.Join(project, ".idea", "data_source_mapping.xml"), `<project version="4"><component name="DataSourcePerFileMappings">
  <file url="file://$PROJECT_DIR$/queries/report.sql" value="`+idPG+`" />
  <file url="file://$PROJECT_DIR$/queries/other.sql" value="`+idMy+`" />
</component></project>`)
	write(filepath.Join(project, "queries", "report.sql"), "select * from orders;")
	write(filepath.Join(project, "queries", "other.sql"), "select 1;")
	write(filepath.Join(cfg, "consoles", "db", idPG, "6f1d6a43-0b5f-4c55-9d3e-1d2b3c4d5e6f.sql"), "select now();")
	write(filepath.Join(cfg, "consoles", "db", idPG, "empty.sql"), "")
	return root, project
}

func TestDiscoverAndConvert(t *testing.T) {
	root, project := fixture(t)
	if dirs := ConfigDirs(root); len(dirs) != 2 || filepath.Base(dirs[0]) != "DataGrip2026.2" {
		t.Fatalf("config dirs = %v", dirs)
	}
	srcs := Discover(root)
	if len(srcs) != 1 || srcs[0].Project != project {
		t.Fatalf("sources = %+v", srcs)
	}
	items, rep := Read(srcs)
	if len(items) != 4 || len(rep.Skipped) != 1 || !strings.Contains(rep.Skipped[0], "erp: unsupported driver") {
		t.Fatalf("items %d, skipped %v", len(items), rep.Skipped)
	}
	got := map[string]Item{}
	for _, it := range items {
		got[it.Conn.Origin.ID] = it
	}
	home, _ := os.UserHomeDir()
	src := project
	want := map[string]config.Connection{
		idPG: {Name: "shop/eu/orders", Driver: "postgres", Host: "db.internal", Port: 6432, User: "app", Database: "orders",
			SSLMode: "require", Params: map[string]string{"application_name": "dg"},
			SSH:    config.SSH{Enabled: true, Host: "bastion", User: "ops", Auth: config.SSHAuthAuto},
			Origin: &config.Origin{Tool: Tool, ID: idPG, Source: src}},
		idMy: {Name: "shop prod", Driver: "mysql", Host: "10.0.0.5", Port: 3306, User: "root", Database: "shop",
			SSLMode: "disable", ReadOnly: true,
			SSH: config.SSH{Enabled: true, Host: "jump.example.com", Port: 2222, User: "deploy", Auth: config.SSHAuthKey,
				KeyPath: filepath.Join(home, ".ssh/prod")},
			Origin: &config.Origin{Tool: Tool, ID: idMy, Source: src}},
		idLite: {Name: "app", Driver: "sqlite", Database: filepath.Join(filepath.Dir(project), "data", "app.db"),
			SSH: config.SSH{Auth: config.SSHAuthAuto}, Origin: &config.Origin{Tool: Tool, ID: idLite, Source: src}},
		idMSSQL: {Name: "crm staging", Driver: "sqlserver", Host: "crm", Port: 14330, User: "sa", Database: "crm",
			SSLMode: "verify-full", SSH: config.SSH{Auth: config.SSHAuthAuto},
			Origin: &config.Origin{Tool: Tool, ID: idMSSQL, Source: src}},
	}
	for id, w := range want {
		if !reflect.DeepEqual(got[id].Conn, w) {
			t.Errorf("%s:\n got %+v\nwant %+v", w.Name, got[id].Conn, w)
		}
	}
	if n := got[idPG].Notes; len(n) != 1 || n[0] != "ignored JDBC parameters: weird" {
		t.Errorf("pg notes = %v", n)
	}
	if n := got[idMy].Notes; len(n) != 0 {
		t.Errorf("mysql notes = %v", n)
	}
	if n := strings.Join(got[idMSSQL].Notes, "; "); !strings.Contains(n, "named instance SQLEXPRESS") || !strings.Contains(n, "SSH tunnel gone") {
		t.Errorf("mssql notes = %v", n)
	}
}

func TestConvertURLs(t *testing.T) {
	cases := []struct {
		url              string
		driver, host, db string
		port             int
		ssl              string
	}{
		{"jdbc:postgresql://[::1]:5433/x", "postgres", "::1", "x", 5433, ""},
		{"jdbc:postgresql:mydb", "postgres", "localhost", "mydb", 5432, ""},
		{"jdbc:postgresql://h/", "postgres", "h", "", 5432, ""},
		{"jdbc:mariadb://u@h:3307/d?sslMode=VERIFY_IDENTITY", "mysql", "h", "d", 3307, "verify-full"},
		{"jdbc:clickhouse://ch/logs", "clickhouse", "ch", "logs", 8123, ""},
		{"jdbc:clickhouse:https://ch/logs", "clickhouse", "ch", "logs", 8443, "require"},
		{"jdbc:ch://ch:8124?database=x&ssl=true", "clickhouse", "ch", "x", 8124, "require"},
		{"jdbc:jtds:sqlserver://ms:1500/erp;encrypt=false", "sqlserver", "ms", "erp", 1500, "disable"},
		{"jdbc:sqlite::memory:", "sqlite", "", ":memory:", 0, ""},
	}
	for _, c := range cases {
		got, _, err := Convert(DataSource{UUID: idPG, Name: "x", URL: c.url}, Source{})
		if err != nil {
			t.Errorf("%s: %v", c.url, err)
			continue
		}
		if got.Driver != c.driver || got.Host != c.host || got.Database != c.db || got.Port != c.port || got.SSLMode != c.ssl {
			t.Errorf("%s = %s %s:%d/%s ssl=%q", c.url, got.Driver, got.Host, got.Port, got.Database, got.SSLMode)
		}
	}
	if c, _, _ := Convert(DataSource{URL: "jdbc:mariadb://u@h/d"}, Source{}); c.User != "u" {
		t.Errorf("user from URL = %q", c.User)
	}
	for _, bad := range []string{"", "postgres://h/db", "jdbc:postgresql://h:x/db", "jdbc:clickhouse:grpc://h"} {
		if _, _, err := Convert(DataSource{URL: bad}, Source{}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSync(t *testing.T) {
	root, project := fixture(t)
	items, rep := Read(Discover(root))
	mine := config.Connection{ID: "mine", Name: "shop prod", Driver: "postgres", Host: "x"}
	conns, rep := Sync([]config.Connection{mine}, items, rep, false)
	if len(rep.Added) != 4 || len(conns) != 5 || conns[0].ID != "mine" {
		t.Fatalf("added %v", rep.Added)
	}
	byID := func(id string) *config.Connection {
		for i := range conns {
			if conns[i].Origin != nil && conns[i].Origin.ID == id {
				return &conns[i]
			}
		}
		return nil
	}
	my := byID(idMy)
	if my.Name != "shop prod (DataGrip)" || my.Env != config.EnvProd || my.ID == "" {
		t.Errorf("collision / env: %+v", my)
	}
	if byID(idLite).Env != config.EnvLocal || byID(idMSSQL).Env != config.EnvStaging || byID(idPG).Env != config.EnvNone {
		t.Error("env guesses")
	}

	// squi-side changes survive a re-import; DataGrip-side changes win
	id := my.ID
	my.Env, my.Label, my.StatementTimeout, my.Host = config.EnvStaging, "EU", "1m", "edited-in-squi"
	pg := byID(idPG)
	pg.ReadOnly = true
	items, rep = Read(Discover(root))
	conns, rep = Sync(conns, items, rep, false)
	my = byID(idMy)
	if my.ID != id || my.Env != config.EnvStaging || my.Label != "EU" || my.StatementTimeout != "1m" || my.Host != "10.0.0.5" {
		t.Errorf("resync: %+v", my)
	}
	if !byID(idPG).ReadOnly {
		t.Error("read-only cleared by a re-import")
	}
	if !slices.Equal(rep.Updated, []string{"shop prod (DataGrip)"}) || len(rep.Unchanged) != 3 || len(rep.Added) != 0 {
		t.Errorf("report: updated %v unchanged %v added %v", rep.Updated, rep.Unchanged, rep.Added)
	}

	// a data source deleted in DataGrip is stale, or removed with prune
	ds := filepath.Join(project, ".idea", "dataSources.xml")
	b, _ := os.ReadFile(ds)
	s := string(b)
	start := strings.Index(s, `<data-source source="LOCAL" name="app"`)
	end := strings.Index(s[start:], "</data-source>") + start + len("</data-source>")
	os.WriteFile(ds, []byte(s[:start]+s[end:]), 0o644)
	items, rep = Read(Discover(root))
	if _, rep2 := Sync(conns, items, rep, false); !slices.Equal(rep2.Stale, []string{"app"}) {
		t.Errorf("stale = %v", rep2.Stale)
	}
	out, rep := Sync(conns, items, rep, true)
	if !slices.Equal(rep.Removed, []string{"app"}) || len(out) != 4 || out[0].ID != "mine" {
		t.Errorf("prune: %v, %d left", rep.Removed, len(out))
	}
	// importing another project never prunes this one's profiles
	if _, rep := Sync(conns, nil, Report{Sources: []string{"/elsewhere"}}, true); len(rep.Removed) != 0 {
		t.Errorf("pruned outside the scanned sources: %v", rep.Removed)
	}
}

func TestQueries(t *testing.T) {
	root, project := fixture(t)
	old := filepath.Join(root, "DataGrip2025.3", "consoles", "db", idPG, "console.sql")
	os.Chtimes(old, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour))
	qs := Queries(root, idPG, project)
	var got []string
	for _, q := range qs {
		got = append(got, q.Title+":"+map[bool]string{true: "console", false: "file"}[q.Console])
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"console:console", "console:console", "report:file"}) || qs[len(qs)-1].Path != old {
		t.Errorf("queries = %v", got)
	}
	if qs := Queries(root, "not-a-uuid", ""); len(qs) != 0 {
		t.Errorf("bad id: %v", qs)
	}
}

func TestSourceAt(t *testing.T) {
	root, project := fixture(t)
	for _, p := range []string{project, filepath.Join(project, ".idea")} {
		s, err := SourceAt(p, root)
		if err != nil || s.Project != project || filepath.Base(s.Config) != "DataGrip2026.2" {
			t.Errorf("%s: %+v %v", p, s, err)
		}
	}
	if _, err := SourceAt(t.TempDir(), root); err == nil {
		t.Error("empty dir accepted")
	}
	if ServiceName(idPG) != "IntelliJ Platform DB — "+idPG {
		t.Error("service name")
	}
}
