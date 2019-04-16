import (
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/davecgh/go-spew/spew"
	// for mysql driver
	_ "github.com/go-sql-driver/mysql"
)

type mysqlReplicaClone struct {
	copyFrom     Instance
	copyTo       Instance
	rootPass     string
	binlogRetHrs *int
	log          *log.Logger
}

const (
	commaSep  = ", "
	pkSep     = "|"
	grantPriv = "Grant_priv"
)

const usersQuery = `
select
    Host
,   User
,   Password
,   Select_priv
,   Insert_priv
,   Update_priv
,   Delete_priv
,   Create_priv
,   Drop_priv
,   Reload_priv
,   Shutdown_priv
,   Process_priv
,   File_priv
,   Grant_priv
,   References_priv
,   Index_priv
,   Alter_priv
,   Show_db_priv
,   Super_priv
,   Create_tmp_table_priv
,   Lock_tables_priv
,   Execute_priv
,   Repl_slave_priv
,   Repl_client_priv
,   Create_view_priv
,   Show_view_priv
,   Create_routine_priv
,   Alter_routine_priv
,   Create_user_priv
,   Event_priv
,   Trigger_priv
,   Create_tablespace_priv
from mysql.user
order by user
`

const grantsQuery = `
select
    Host
,   Db
,   User
,   Select_priv
,   Insert_priv
,   Update_priv
,   Delete_priv
,   Create_priv
,   Drop_priv
,   Grant_priv
,   References_priv
,   Index_priv
,   Alter_priv
,   Create_tmp_table_priv
,   Lock_tables_priv
,   Create_view_priv
,   Show_view_priv
,   Create_routine_priv
,   Alter_routine_priv
,   Execute_priv
,   Event_priv
,   Trigger_priv
from mysql.db
`

func (c *mysqlReplicaClone) execute(verbose, dryRun bool) error {
	if err := c.copyFrom.connect("root", c.rootPass, "mysql"); err != nil {
		return err
	}
	defer c.copyFrom.DB.Close()

	if err := c.copyTo.connect("root", c.rootPass, "mysql"); err != nil {
		return err
	}
	defer c.copyTo.DB.Close()

	usersPK := map[string]interface{}{"User": nil, "Host": nil}
	srcUsers, err := c.copyFrom.dumpQuery(usersQuery, usersPK)
	if err != nil {
		return err
	}

	grantsPK := map[string]interface{}{"Host": nil, "Db": nil, "User": nil}
	srcGrants, err := c.copyFrom.dumpQuery(grantsQuery, grantsPK)
	if err != nil {
		return err
	}

	trgUsers, err := c.copyTo.dumpQuery(usersQuery, usersPK)
	if err != nil {
		return err
	}

	// if verbose {
	// 	spewConfig := spew.ConfigState{
	// 		Indent:                  "\t",
	// 		DisablePointerAddresses: true,
	// 		DisableCapacities:       true,
	// 		SortKeys:                true,
	// 	}
	// 	spewConfig.Dump(srcUsers)
	// 	spewConfig.Dump(srcGrants)
	// 	spewConfig.Dump(trgUsers)
	// }

	for user, privs := range srcUsers {
		if _, ok := trgUsers[user]; ok {
			continue
		}

		cmd := createUserCMD(privs)
		c.log.Printf("... mysqlReplicaClone.execute: [%24s] creating %q user: %q", c.copyTo.Name, user, cmd)
		if verbose {
			spew.Dump(privs)
		}
		if !dryRun {
			if _, err := c.copyTo.DB.Exec(cmd); err != nil {
				return err
			}
		}

		// check if this user/host combo has any schema grants we need to bring over,
		// and yes, we end up going over the entire srcGrants map for every user, but
		// only when a user is missing in new replica, which are few and far between,
		// so I am not that concerned about performance here ... however there is a
		// way to improve this, see TODO(vm) under dumpQuery() func ...
		for key, grants := range srcGrants {
			if !hasGrants(key, *privs["Host"], *privs["User"]) {
				continue
			}

			cmd := giveGrantsCMD(grants)
			c.log.Printf("... mysqlReplicaClone.execute: [%24s] granting privs to %q user: %q", c.copyTo.Name, user, cmd)
			if verbose {
				spew.Dump(grants)
			}
			if !dryRun {
				if _, err := c.copyTo.DB.Exec(cmd); err != nil {
					return err
				}
			}
		}
	}

	return c.setBinlogRetention(verbose, dryRun)
}

func (c *mysqlReplicaClone) setBinlogRetention(verbose, dryRun bool) error {
	// getting multi-row results from stored procedure is not supported with this driver:
	// see:
	//	https://github.com/go-sql-driver/mysql/issues/66
	//	https://github.com/golang/go/issues/12382
	//
	// pk := map[string]interface{}{"name": nil}
	// conf, err := c.copyFrom.dumpQuery("call mysql.rds_show_configuration", pk)
	// if err != nil {
	// 	return fmt.Errorf("can't get binlog retention hours on %q: %v", c.copyFrom.Name, err)
	// }

	// row, ok := conf["binlog retention hours"]
	// if verbose {
	// 	spew.Dump(row)
	// }
	// if !ok {
	// 	return nil
	// }

	hrs := 168 // defaults to 7 days
	if c.binlogRetHrs != nil {
		hrs = *c.binlogRetHrs
	}
	cmd := fmt.Sprintf("call mysql.rds_set_configuration('binlog retention hours', %d)", hrs)
	c.log.Printf("... mysqlReplicaClone.setBinlogRetention: [%24s] cmd: %q", c.copyTo.Name, cmd)

	if dryRun {
		return nil
	}

	if _, err := c.copyTo.DB.Exec(cmd); err != nil {
		return fmt.Errorf("can't set binlog retention hours on %q: %v", c.copyTo.Name, err)
	}

	return nil
}

func hasGrants(grantKey, host, user string) bool {
	if strings.HasPrefix(grantKey, host+pkSep) && strings.HasSuffix(grantKey, pkSep+user+pkSep) {
		return true
	}
	return false
}

func (i *Instance) connect(user, pass, schema string) error {
	host := *i.RDSDBInstance.Endpoint.Address
	port := *i.RDSDBInstance.Endpoint.Port
	conn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?interpolateParams=true", user, pass, host, port, schema)
	mskd := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?interpolateParams=true", user, "*******", host, port, schema)
	db, err := sql.Open("mysql", conn)
	if err != nil {
		return fmt.Errorf("ERROR: connecting to %s: %v", mskd, err)
	}

	err = db.Ping()
	if err != nil {
		return fmt.Errorf("ERROR: can't ping DB %s: %v", mskd, err)
	}

	// See:
	//	https://github.com/go-sql-driver/mysql/issues/657
	// 	https://github.com/go-sql-driver/mysql/issues/670
	db.SetConnMaxLifetime(time.Second * 10)
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(4)

	i.DB = db
	return nil
}

// TODO(vm): add a groupBy parameter that'll aggreate primary keys by groupBy keys
// this will require a new return type where the group by will be embedded ...
func (i *Instance) dumpQuery(query string, pk map[string]interface{}) (result map[string]map[string]*string, err error) {
	result = make(map[string]map[string]*string)

	// Execute the query
	rows, err := i.DB.Query(query)
	if err != nil {
		return nil, err
	}

	defer func() {
		closeErr := rows.Close()
		// prefer to return unloadTable() error
		if err == nil {
			err = closeErr
		}
	}()

	// Get column names
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	// Make a slice for the values
	values := make([]sql.RawBytes, len(columns))

	// rows.Scan wants '[]interface{}' as an argument, so we must copy the
	// references into such a slice
	// See http://code.google.com/p/go-wiki/wiki/InterfaceSlice for details
	scanArgs := make([]interface{}, len(values))
	for i := range values {
		scanArgs[i] = &values[i]
	}

	// Fetch rows
	for rows.Next() {
		row := make(map[string]*string)

		// get RawBytes from data
		err = rows.Scan(scanArgs...)
		if err != nil {
			return nil, err
		}

		// pkValue is a composite primary key value built
		// based on passed in pk (columns) as:
		// 	pk1Val|pk2Val|pkNVal
		var pkValue string

		// Now stuff the data into a map of column names to their values
		// but first convert it to a real base type unless it's a nil
		for k, v := range values {
			var x *string
			if v == nil {
				x = nil
			} else {
				x = strToBaseType(string(v))
			}

			colName := columns[k]

			row[colName] = x

			if _, found := pk[colName]; found {
				pkValue += *x + pkSep
			}
		}

		result[pkValue] = row
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

func createUserCMD(privs map[string]*string) string {
	allPrivs := []string{
		"Select_priv",
		"Insert_priv",
		"Update_priv",
		"Delete_priv",
		"Create_priv",
		"Drop_priv",
		"Reload_priv",
		"Shutdown_priv",
		"Process_priv",
		"File_priv",
		"References_priv",
		"Index_priv",
		"Alter_priv",
		"Show_db_priv",
		"Super_priv",
		"Create_tmp_table_priv",
		"Lock_tables_priv",
		"Execute_priv",
		"Repl_slave_priv",
		"Repl_client_priv",
		"Create_view_priv",
		"Show_view_priv",
		"Create_routine_priv",
		"Alter_routine_priv",
		"Create_user_priv",
		"Event_priv",
		"Trigger_priv",
		"Create_tablespace_priv",
	}

	sort.Strings(allPrivs)

	cmd := ""
	cnt := 0
	for _, name := range allPrivs {
		c := decodePriv(name, privs[name])
		if c == "" {
			continue
		}
		cnt++
		cmd += c + commaSep
	}

	switch cnt {
	case len(allPrivs):
		cmd = "ALL PRIVILEGES"
	case 0:
		cmd = "USAGE"
	default:
		cmd = strings.TrimSuffix(cmd, commaSep)
	}

	return fmt.Sprintf("GRANT %s ON *.* TO '%s'@'%s' IDENTIFIED BY PASSWORD '%s'%s",
		cmd,
		*privs["User"],
		*privs["Host"],
		*privs["Password"],
		decodePriv(grantPriv, privs[grantPriv]),
	)
}

func giveGrantsCMD(privs map[string]*string) string {
	allPrivs := []string{
		"Select_priv",
		"Insert_priv",
		"Update_priv",
		"Delete_priv",
		"Create_priv",
		"Drop_priv",
		"References_priv",
		"Index_priv",
		"Alter_priv",
		"Create_tmp_table_priv",
		"Lock_tables_priv",
		"Create_view_priv",
		"Show_view_priv",
		"Create_routine_priv",
		"Alter_routine_priv",
		"Execute_priv",
		"Event_priv",
		"Trigger_priv",
	}

	sort.Strings(allPrivs)

	cmd := ""
	cnt := 0
	for _, name := range allPrivs {
		c := decodePriv(name, privs[name])
		if c == "" {
			continue
		}
		cnt++
		cmd += c + commaSep
	}

	switch cnt {
	case len(allPrivs):
		cmd = "ALL PRIVILEGES"
	case 0:
		return ""
	default:
		cmd = strings.TrimSuffix(cmd, commaSep)
	}

	return fmt.Sprintf("GRANT %s ON `%s`.* TO '%s'@'%s'%s",
		cmd,
		*privs["Db"],
		*privs["User"],
		*privs["Host"],
		decodePriv(grantPriv, privs[grantPriv]),
	)
}

// decodePriv - decodes mysql.[user|db].*_priv column's key/value pair
// to it's GRANT statement equivalent based on:
//	https://dev.mysql.com/doc/refman/8.0/en/privileges-provided.html
func decodePriv(key string, value *string) string {
	decoder := map[string]string{
		"Alter_priv":             "ALTER",
		"Alter_routine_priv":     "ALTER ROUTINE",
		"Create_priv":            "CREATE",
		"Create_role_priv":       "CREATE ROLE",
		"Create_routine_priv":    "CREATE ROUTINE",
		"Create_tablespace_priv": "CREATE TABLESPACE",
		"Create_tmp_table_priv":  "CREATE TEMPORARY TABLES",
		"Create_user_priv":       "CREATE USER",
		"Create_view_priv":       "CREATE VIEW",
		"Delete_priv":            "DELETE",
		"Drop_priv":              "DROP",
		"Drop_role_priv":         "DROP ROLE",
		"Event_priv":             "EVENT",
		"Execute_priv":           "EXECUTE",
		"File_priv":              "FILE",
		"Grant_priv":             " WITH GRANT OPTION", // yes there is space prefix here
		"Index_priv":             "INDEX",
		"Insert_priv":            "INSERT",
		"Lock_tables_priv":       "LOCK TABLES",
		"Process_priv":           "PROCESS",
		"References_priv":        "REFERENCES",
		"Reload_priv":            "RELOAD",
		"Repl_client_priv":       "REPLICATION CLIENT",
		"Repl_slave_priv":        "REPLICATION SLAVE",
		"Select_priv":            "SELECT",
		"Show_db_priv":           "SHOW DATABASES",
		"Show_view_priv":         "SHOW VIEW",
		"Shutdown_priv":          "SHUTDOWN",
		"Super_priv":             "SUPER",
		"Trigger_priv":           "TRIGGER",
		"Update_priv":            "UPDATE",
	}

	if value == nil {
		return ""
	}

	if *value == "N" {
		return ""
	}

	return decoder[key]
}

func strToBaseType(s string) *string {
	return &s
}
