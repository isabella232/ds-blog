# Enable RDS Encryption At Rest For Existing Instance

During this year's SOC-2 Audit, we received an urgent requirement that all of our production databases had to have **Encryption At Rest** enabled.  The deadline was set to 1 week.  Our quick inventory check revealed that we had 10 MySQL database instances hosted on AWS RDS to encrypt and 3 of them were replicas.

To make matters worse, one of these databases was our monolith cluster supporting majority of the customer base.  And as you can imagine with a monolith database - there are a lot of legacy services that depend on it, which makes a proposion of making a test-clone of the **entire** environment practically impossible.  Which meant we had to do get the process 100% right the first time around, or risk loosing data.  Needless to say - this put a lot of pressure on our team (Data Services).

Armed with this information we got on a call with our security team, and made an analogy, that what we are attempting to do here is like swapping foundation from underneath a skyscraper while the tenants are still inside.  Sure, we are running on AWS RDS, and they provide us the heavy "*earth moving*" equipment for majority of the work we need to do, but it still requires very careful, surgical like precision to execute this process to not miss any details.  This analogy made the right impression and we had our deadline extened to 2 weeks.  

What follows is the story of how we got this done on time, first go, and with zero data loss.

## Options

There are a number of important details you have to get right to enable Encryption At Rest on an **existing** RDS MySQL instance, but before delving into these details, lets answer some common questions:

Why can't you simply enable Encryption At Rest on an existing instance by turning the **Encryption Enabled** switch to `ON`?  Here's what AWS documetation has to say about that:

> You can only enable encryption for an Amazon RDS DB instance when you create it, not after the DB instance is created.
>
> However, because you can encrypt a copy of an unencrypted DB snapshot, you can effectively add encryption to an unencrypted DB instance. That is, you can create a snapshot of your DB instance, and then create an encrypted copy of that snapshot. You can then restore a DB instance from the encrypted snapshot, and thus you have an encrypted copy of your original DB instance.

REF: [Encryption.Limitations](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/Overview.Encryption.html#Overview.Encryption.Limitations)

You also cannot create an encrypted Read Replica from an unencrypted DB instance:

> You cannot create an encrypted Read Replica from an unencrypted DB instance. (Service: AmazonRDS; Status Code:
>
>  400; Error Code: InvalidParameterCombination; Request ID: 943905dc-116c-4f7b-9963-e0c5766fd92f)

![cannot-create-an-encrypted-Read-Replica](cannot-create-an-encrypted-Read-Replica.png)

What's the solution then?  There are at least two methods to enable Encryption At Rest for an existing RDS instance:

1. **Limited Downtime**: encrypting and promoting a read replica
2. **Complete Downtime**: cold backup / restore approach

We picked **cold backup / restore approach** due to it's lower complexity to develop proper automation and because our SLA permits a [scheduled downtime](https://support.invisionapp.com/hc/en-us/articles/360025579551) on the weekends.  However, both methods share the same important step: *restoring an RDS Instance from an encrypted snapshot*.  The only difference is that method 1) uses the snapshot of the read-replica and method 2) the snapshot of the master/primary itself.

And there are some important gaps in the [AWS RDS API RestoreDBInstanceFromDBSnapshot](https://docs.aws.amazon.com/AmazonRDS/latest/APIReference/API_RestoreDBInstanceFromDBSnapshot.html) that have to be filled with proper automation which we'll delve into in this blog post.  But before getting there, lets go over the high level process overview of the **cold backup / restore approach** we went through.

## Cold Backup / Restore Approach

1. [Enable site wide maintenance](https://support.invisionapp.com/hc/en-us/articles/360025579551)
2. Shutdown all services
3. Shutdown analytics processes
4. Rename the databases with `old-` prefix
5. Bounce the databases to clear any connections
6. Verify no connections exist
7. Take an RDS snapshot across all databases
8. Encrypt snapshots
9. Restore encrypted snapshots with `new-` prefix
10. Recreate replicas and analytics users and setup
11. Rename all newly encrypted instances and their replicas back to their original names
12. Restart all services
13. Restart/validate analytics processes
14. Validate functionality
15. Disable site wide maintenance

The interesting bits are in steps 9 and 10 and that's what we'll focus on next.

## Restore Encrypted Snapshots

There are some important gaps in the [AWS RDS API RestoreDBInstanceFromDBSnapshot](https://docs.aws.amazon.com/AmazonRDS/latest/APIReference/API_RestoreDBInstanceFromDBSnapshot.html) that have to be filled with proper automation.  Our infrastructure automation language of choice is [Go](https://golang.org/) so we'll use [real snippets of code](https://github.com/InVisionApp/ds-blog/tree/master/blog/DS-1311/code) from our tooling to go over these gaps.

In order to restore an RDS instance from an encrypted snapshot we construct an API call and copy the following parameters directly from the source instance:

```go
type Instance struct {
	Name                  string
	MultiAZ               bool
	Engine                string
	EngineVersion         string
	DBInstanceClass       string
	AllocatedStorage      int64
	NewDBInstanceClass    string
	Status                string
	ParGroupName          string
	ParGroupStatus        string
	PreferredBackupWindow string
	BackupRetentionPeriod int64
	RDSDBInstance         *rds.DBInstance
	TagList               []*rds.Tag
	DB                    *sql.DB
}
...
...
...
func (s *SDK) RestoreInstance(sorceInstance Instance, takeFreshSnap bool, kmsKeyID string, np *NameParser) (Instance, error) {
	targetName := np.NewName(sorceInstance.Name)
	dbParGroupName := sorceInstance.RDSDBInstance.DBParameterGroups[0].DBParameterGroupName
	vpcSecurityGroups := sorceInstance.FilterVPCSecurityGroups(Active)
...
...
	snapInput := &rds.RestoreDBInstanceFromDBSnapshotInput{
		AutoMinorVersionUpgrade: sorceInstance.RDSDBInstance.AutoMinorVersionUpgrade,
		CopyTagsToSnapshot:          sorceInstance.RDSDBInstance.CopyTagsToSnapshot,
		DBInstanceClass:             sorceInstance.RDSDBInstance.DBInstanceClass,
		DBInstanceIdentifier:        aws.String(targetName),
		DBSnapshotIdentifier:        snap.DBSnapshotIdentifier,
		DBSubnetGroupName:           sorceInstance.RDSDBInstance.DBSubnetGroup.DBSubnetGroupName,
		Iops:                        sorceInstance.RDSDBInstance.Iops,
		MultiAZ:                     sorceInstance.RDSDBInstance.MultiAZ,
		EnableCloudwatchLogsExports: sorceInstance.RDSDBInstance.EnabledCloudwatchLogsExports,
		OptionGroupName:             sorceInstance.RDSDBInstance.OptionGroupMemberships[0].OptionGroupName,
		PubliclyAccessible:          sorceInstance.RDSDBInstance.PubliclyAccessible,
		StorageType:                 sorceInstance.RDSDBInstance.StorageType,
		Tags:                        sorceInstance.TagList,
	}
	if !*sorceInstance.RDSDBInstance.MultiAZ {
		snapInput.AvailabilityZone = sorceInstance.RDSDBInstance.AvailabilityZone
	}
	if _, err := s.svc.RestoreDBInstanceFromDBSnapshotWithContext(s.ctx, snapInput); err != nil {
		return Instance{}, fmt.Errorf("ERROR: RestoreDBInstanceFromDBSnapshotWithContext(%s) failed with: %v", *snap.DBSnapshotIdentifier, err)
	}

	return s.ModifyInstance(targetName, *dbParGroupName, vpcSecurityGroups)
```

Note that:

1. <u>if not supplied</u>, `MultiAZ`, `OptionGroupName` and `DBSubnetGroupName` will default to values that won't match the source instance
2. to match `DBParameterGroupName` and `VpcSecurityGroupIds` we have to call a [ModifyDBInstance API](https://docs.aws.amazon.com/AmazonRDS/latest/APIReference/API_ModifyDBInstance.html) - there is no way to do this during restore itself:

```go
func (s *SDK) ModifyInstance(instanceName, dbParGroupName string, vpcSecurityGroups []*string) (Instance, error) {
	readyFunc := func(i Instance) bool {
		ok := i.Status == Available
		if !ok {
			s.log.Printf("... ModifyInstance: [%24s] waiting for DB status to change from %q to %q", i.Name, i.Status, Available)
		}
		return ok
	}

	if err := s.waitForDBStatus(instanceName, readyFunc); err != nil {
		return Instance{}, err
	}

	// check if the groupName is already set to what we want
	i, err := s.Describe(instanceName)
	if err != nil {
		return Instance{}, err
	}

	// the only way it can be set to dbParGroupName is if we already modified it,
	// and in this case we can make an educated guess that the DBSecurityGroups is also
	// set to the right value so we are not even checking it here
	if *i.RDSDBInstance.DBParameterGroups[0].DBParameterGroupName == dbParGroupName {
		s.log.Printf("... ModifyInstance: [%24s] DBParameterGroupName is already set to %q", instanceName, dbParGroupName)
		return i, nil
	}

	pp := func(a []*string) string {
		r := ""
		for _, v := range a {
			r += *v + " "
		}
		return strings.TrimSpace(r)
	}

	s.log.Printf("... ModifyInstance: [%24s] Updating DBParameterGroupName to: %q and SecurityGroups to: %q", instanceName, dbParGroupName, pp(vpcSecurityGroups))
	if _, err := s.svc.ModifyDBInstanceWithContext(s.ctx, &rds.ModifyDBInstanceInput{
		DBInstanceIdentifier: aws.String(instanceName),
		ApplyImmediately:     aws.Bool(true),
		DBParameterGroupName: aws.String(dbParGroupName),
		VpcSecurityGroupIds:  vpcSecurityGroups,
	}); err != nil {
		return Instance{}, err
	}

	if err := s.waitForDBStatus(instanceName, readyFunc); err != nil {
		return Instance{}, err
	}

	// DB instance has to be bounced now, but we need to wait until
	// parameter group status == pending-reboot before doing so ...
	readyFunc2 := func(i Instance) bool {
		ok := i.ParGroupStatus == pendingReboot
		if !ok {
			s.log.Printf("... ModifyInstance: [%24s] waiting for %s group status change, want: %s got: %s", instanceName, dbParGroupName, pendingReboot, i.ParGroupStatus)
		}
		return ok
	}
	if err := s.waitForDBStatus(instanceName, readyFunc2); err != nil {
		return Instance{}, err
	}

	// reboot (no failover as per doc)
	s.log.Printf("... ModifyInstance: [%24s] Rebooting, for DBParameterGroupName %q to take effect", instanceName, dbParGroupName)
	if err := s.Reboot(instanceName, false); err != nil {
		return Instance{}, err
	}

	if err := s.waitForDBStatus(instanceName, readyFunc); err != nil {
		return Instance{}, err
	}

	return s.Describe(instanceName)
}
```

In the code snippet above, we are calling `waitForDBStatus` twice: the first pass is to ensure the newly restored instance is in `Available` state before doing any changes, and the second time to wait for the reboot to happen before returning back to the caller.  The reboot is needed for `DBParameterGroupName` change to take effect.  If you need the code for `waitForDBStatus`, it's available here: [waitForDBStatus source code](https://github.com/InVisionApp/ds-blog/blob/master/blog/DS-1311/code/wait_for_dbstatus.go)

## Recreate Read Replicas
At this point, the instance is restored from the encrypted snapshot and all of it's attributes match the source instance it's replacing (we'll go over how to validate this in the following section).  The next step is to recreate all of it's read-replicas if any exist.  This task can require substantial custom automation depending on how these read-replicas were originally setup.  For example, we had to redo and automate the following:

1. re-create any DB users that were created directly on the original replica
2. re-set [binlog retention hours](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/mysql_rds_show_configuration.html) that were configured on the original replica

Lets go over these in order:

```go
func (s *SDK) reCreateReplica(master, copyFrom Instance, name string, binlogRetention int, rootPass string) error {
	for _, replica := range master.RDSDBInstance.ReadReplicaDBInstanceIdentifiers {
		if *replica == name {
			s.log.Printf("... reCreateReplica: [%24s] %q replica already exists", master.Name, name)
			return s.reCreateReplicaFinalize(master, copyFrom, name, binlogRetention, rootPass)
		}
	}

	replicaInput := &rds.CreateDBInstanceReadReplicaInput{
		AutoMinorVersionUpgrade: copyFrom.RDSDBInstance.AutoMinorVersionUpgrade,
		CopyTagsToSnapshot:   copyFrom.RDSDBInstance.CopyTagsToSnapshot,
		DBInstanceClass:      copyFrom.RDSDBInstance.DBInstanceClass,
		DBInstanceIdentifier: aws.String(name),
		// DBSubnetGroupNotAllowedFault: DbSubnetGroupName should not be specified for read replicas that are created in the same region as the master
		// DBSubnetGroupName:           copyFrom.RDSDBInstance.DBSubnetGroup.DBSubnetGroupName,
		EnableCloudwatchLogsExports: copyFrom.RDSDBInstance.EnabledCloudwatchLogsExports,
		EnablePerformanceInsights:   copyFrom.RDSDBInstance.PerformanceInsightsEnabled,
		Iops:                        copyFrom.RDSDBInstance.Iops,
		KmsKeyId:                    copyFrom.RDSDBInstance.KmsKeyId,
		MonitoringInterval:          copyFrom.RDSDBInstance.MonitoringInterval,
		MonitoringRoleArn:           copyFrom.RDSDBInstance.MonitoringRoleArn,
		MultiAZ:                     copyFrom.RDSDBInstance.MultiAZ,
		OptionGroupName:             copyFrom.RDSDBInstance.OptionGroupMemberships[0].OptionGroupName,
		PerformanceInsightsKMSKeyId: copyFrom.RDSDBInstance.PerformanceInsightsKMSKeyId,
		PubliclyAccessible:         copyFrom.RDSDBInstance.PubliclyAccessible,
		SourceDBInstanceIdentifier: master.RDSDBInstance.DBInstanceIdentifier,
		StorageType:                copyFrom.RDSDBInstance.StorageType,
		Tags:                       copyFrom.TagList}
	if !*copyFrom.RDSDBInstance.MultiAZ {
		replicaInput.AvailabilityZone = copyFrom.RDSDBInstance.AvailabilityZone
	}
	if *copyFrom.RDSDBInstance.DbInstancePort > 0 {
		replicaInput.Port = copyFrom.RDSDBInstance.DbInstancePort
	}

	s.log.Printf("... reCreateReplica: [%24s] creating %q replica based on %q", master.Name, name, copyFrom.Name)
	if _, err := s.svc.CreateDBInstanceReadReplicaWithContext(s.ctx, replicaInput); err != nil {
		return err
	}

	return s.reCreateReplicaFinalize(master, copyFrom, name, binlogRetention, rootPass)
}
```
The code snippet above is similar to the *restore instance from snapshot* we did earlier, we are looping over all existing read-replicas that exist on the source instance and re-creating them with the same attributes by calling [CreateDBInstanceReadReplica API](https://docs.aws.amazon.com/goto/WebAPI/rds-2014-10-31/CreateDBInstanceReadReplica).  Then, before returning back to caller, we finalize the replica's AWS attributes and DB's internals (users and replication parameters) to match it's source by calling `reCreateReplicaFinalize`:

```go
func (s *SDK) reCreateReplicaFinalize(master, copyFrom Instance, name string, binlogRetention int, rootPass string) error {
	groupName := copyFrom.RDSDBInstance.DBParameterGroups[0].DBParameterGroupName
	newReplica, err := s.ModifyInstance(name, *groupName, copyFrom.FilterVPCSecurityGroups(Active))
	if err != nil {
		return err
	}
	return s.cloneReplica(master, copyFrom, newReplica, binlogRetention, rootPass)
}
```

which first calls `ModifyInstance` [routine](https://github.com/InVisionApp/ds-blog/blob/master/blog/DS-1311/code/modify_instance.go) we went over earlier and then `cloneReplica`:

```go
func (s *SDK) cloneReplica(master, copyFrom, newReplica Instance, binlogRetention int, rootPass string) error {
	// for now only support mysql
	if newReplica.Engine != "mysql" {
		return nil
	}

	rc := &mysqlReplicaClone{
		copyFrom:     copyFrom,
		copyTo:       newReplica,
		rootPass:     rootPass,
		binlogRetHrs: aws.Int(binlogRetention),
		log:          s.log,
	}
	dryRun := false
	return rc.execute(s.Verbose, dryRun)
}
```
where `mysqlReplicaClone.execute` does all of the heavy lifting to match DB's internals, the code for which is available in [mysql_replica_clone.go](https://github.com/InVisionApp/ds-blog/blob/master/blog/DS-1311/code/mysql_replica_clone.go).  The goal of this clone process is to take a list of DB users from source replica instance and compare it with the target and re-create any missing users and their grants.  Here's how this works:

- after making [connections](https://github.com/InVisionApp/ds-blog/blob/master/blog/DS-1311/code/mysql_replica_clone.go#L94-L102) to source and target instances, we gather the users and their grants on both source and target instances using `dumpQuery` function [here](https://github.com/InVisionApp/ds-blog/blob/master/blog/DS-1311/code/mysql_replica_clone.go#L104-L119)
- [dumpQuery](https://github.com/InVisionApp/ds-blog/blob/master/blog/DS-1311/code/mysql_replica_clone.go#L246-L324)'s job is to turn MySQL table rows into Go's `map` with it's **key** comprised of *concatenated primary key values* and it's **value** being the entire row represented as `map` with *column names as keys and column values as pointer values*.  For example, suppose the table is:

```
PK1 	PK2 	COL1	COL2
-----	-----	-----	-----
p1A 	p2A 	c1A 	c2A
p1B 	p2B 	c1B 	c2B
p1C 	p2C 	c1C 	NULL
```

then the resulting Go's map structure is as follows:

```
[p1A|p2A]{
  [PK1]*p1A
  [PK2]*p2A
  [COL1]*c1A
  [COL2]*c2A
}
[p1B|p2B]{
  [PK1]*p1B
  [PK2]*p2B
  [COL1]*c1B
  [COL2]*c2B
}
[p1C|p2C]{
  [PK1]*p1C
  [PK2]*p2C
  [COL1]*c1C
  [COL2]*<nil>
}
```

having this data structure allows us to lookup any row by it's primary key and then lookup each column by it's name.  This is very powerful.

* at this point we have our source and target users and all of the source schema level grants, we now compare the list of users and if the user is missing on the target - we re-create it and it's schema level grants (if any) [here](https://github.com/InVisionApp/ds-blog/blob/master/blog/DS-1311/code/mysql_replica_clone.go#L133-L170)
* and as a final step we reset the [binlog retention hours](https://github.com/InVisionApp/ds-blog/blob/master/blog/DS-1311/code/mysql_replica_clone.go#L172) to a static value we always use (7 days)

## Validation
At this point, all newly restored instances with encryption at rest enabled, and it's read-replicas (if any), are ready and named as `new-<name>`.  

Our original instances are renamed as `old-<name>` to ensure no connections can be made to them.  

Before we rename the `new*` to their "real" names, we have to validate that the AWS RDS Attributes match across the board.  And for this task, we'll use python because it has a very powerful [difflib](https://docs.python.org/3/library/difflib.html) Standard Library:

```python
import boto3
import difflib
import pprint


rds = boto3.client('rds', region_name='us-east-1')


def check_instances(instance_pair):
    i1 = instance_pair[0]
    i2 = instance_pair[1]
    if i1 == i2:
        return
    print("###\n####\n##### %s vs %s #####\n####\n###" % (i1, i2))
    resp_a = rds.describe_db_instances(DBInstanceIdentifier=i1)
    resp_b = rds.describe_db_instances(DBInstanceIdentifier=i2)
    diff = difflib.ndiff(
        pprint.pformat(resp_a['DBInstances'][0]).splitlines(keepends=True),
        pprint.pformat(resp_b['DBInstances'][0]).splitlines(keepends=True))
    print(''.join(diff))


instances = [
    ("old-prod-one", "new-old-prod-one"),
    ("old-prod-one-replica", "new-old-prod-one-replica"),
    ("old-prod-one-replica2", "new-old-prod-one-replica2"),
    ("old-prod-two", "new-old-prod-two"),
    ("old-prod-three", "new-old-prod-three"),
    ("old-prod-three-replica", "new-old-prod-three-replica"),
]

for instance_pair in instances:
    check_instances(instance_pair)
```

to run validation we executed the above script and used `grep` to filter the differences:

```bash
python compare_instances.py | egrep "^#|^\+"
```

the differences were what we expected (names, etc), so we simply renamed the `new-*` instances to their original names using AWS CLI:

```bash
aws rds modify-db-instance --db-instance-identifier new-old-prod-one           --new-db-instance-identifier prod-one --apply-immediately
aws rds modify-db-instance --db-instance-identifier new-old-prod-one-replica   --new-db-instance-identifier prod-one-replica --apply-immediately
aws rds modify-db-instance --db-instance-identifier new-old-prod-one-replica2  --new-db-instance-identifier prod-one-replica2 --apply-immediately
aws rds modify-db-instance --db-instance-identifier new-old-prod-two           --new-db-instance-identifier prod-two --apply-immediately
aws rds modify-db-instance --db-instance-identifier new-old-prod-three         --new-db-instance-identifier prod-three --apply-immediately
aws rds modify-db-instance --db-instance-identifier new-old-prod-three-replica --new-db-instance-identifier prod-three-replica --apply-immediately
```

**TIP**: you don't have to wait for the master rename to complete before executing the replica rename.

## Lessons Learned

Our production deployment worked as expected and took exactly 4 hours as planned because we rehearsed it 3 times ahead of time so there would be no surprises.  However, there were a couple of things we learned after the fact that you might find useful:

1. There is a slight increase in R/W latency after enabling Encryption At Rest - our slow query monitor picked that up on the first business morning, right after migration (see graph and explanation below).
2. The read replicas we recreated serve as the source for our Data Science department, who use it for analytics type reporting.  Their tooling depends on the precise offset position in the MySQL binlog replication log, and it didn't support the change in the offset that occured as part of replica rebuild process, so it had to go through a full resync.

### Slow Queries
![slow-queries-went-up-after-encryption-at-rest-AWS-RDS](slow-queries-went-up-after-encryption-at-rest-AWS-RDS.png)
When we saw this graph in the morning - it prompted a deep dive analysis with the help of our custom database performance monitor `sentinel`.  One of sentinel's responsibilities is to snapshot MySQL Performance Schema's `events_statements_summary_by_digest` table in order to track aggregated metrics for each SQL query over a period of time.  

Armed with the **before** and **after** snapshots - we quickly determined that there was indeed an increase in the number of slow queries after enabling encryption at rest.  We went from ~5-10 slow queries in a 5 minute period to ~20-50.  This isn't a big problem for our use-case, but we had to carefully identify them to make this determination.  Here's how we did that:

similar queries such as these three:

```
select a, b, c from table where a = '1'; /* took 100ms */
select a, b, c from table where a = '2'; /* took 200ms */ 
select a, b, c from table where a = '3'; /* took 150ms */
```

are aggregated into a single `digest` in the `events_statements_summary_by_digest` table:

```
select a, b, c from table where a = ?;
```

Then, a total time it took to execute all 3 of them is summed up to 450ms and the worst performing query's response time is recorded under MaxTimerWait column, which in this case is 200ms.  Knowing how this works we can use the value of the MaxTimerWait as the metric to find all of the slow queries that ran over a certain threshold, so that's exactly what we did:

According to our graph the highest number of slow queries occurred at 8:37 PDT (~50 slow queries).   We first listed all of the available sentinel snapshots covering this time range:

```
$ sentinel-cli list -d *******.****.******.rds.amazonaws.com -r 2019-03-27T08:10:00/60
Local Time: Thu, 28 Mar 2019 16:38:56 PDT
Parsed Time Range:
 (PDT): 2019-03-27T08:10:00 -- 2019-03-27T09:10:00
 (UTC): 2019-03-27T15:10:00 -- 2019-03-27T16:10:00
Snap ID                               Snap Date         CPU Free(MB) Swap(MB)  Active   Total     Max
------------------------------------- -------------- ------ -------- -------- ------- ------- -------
796f416c-a33b-432f-95e7-41fd63dd0888  03/27 08:12:47  12.66   142013        0      11    1444   16000
17555e43-586a-45d0-b110-7e2c8f7e3c82  03/27 08:17:47  11.87   141924        0      20    1069   16000
ab9e8352-88b6-4808-843e-2ec8193864c8  03/27 08:22:48  11.82   142048        0      32    1100   16000
448ea997-2c47-4fff-a562-9e2e871c8777  03/27 08:27:48  14.71   142062        0      14    1339   16000
dd532074-a1cb-4498-bc82-46145dd0b80d  03/27 08:32:48  12.28   142001        0      13    1893   16000
50b03ded-24b4-41b0-a7f7-b6e516da8b7a  03/27 08:37:47  12.39   141805        0       5    2460   16000 <===
ce3dfe49-4508-482a-9dd5-4626ccaaeb63  03/27 08:42:48  12.39   141648        0      12    1337   16000
66da7798-bac1-4ea0-b33e-44bd204ab27c  03/27 08:47:48  12.04   141807        0      10    1349   16000
67c7bda0-8980-4d0e-957f-2c0eac68ff8a  03/27 08:52:48  11.43   141990        0      10    1261   16000
b77b402c-83dc-44c2-a0e0-64ce5859d25c  03/27 08:57:48  11.97   141929        0      24    1232   16000
e2d154e6-cadb-4c33-a09e-e1a223992536  03/27 09:02:48  12.30   141983        0      13    1340   16000
db4619b6-362b-418d-b3f5-0377b019b475  03/27 09:07:48  11.35   141973        0      18    1354   16000
```
based on the time in question, the snapshot we were after was `50b03ded-24b4-41b0-a7f7-b6e516da8b7a`, so we dug deeper into this snapshot and gave it the *slow query threshold* of (900ms) with the `-w` flag:
```
$ sentinel-cli list -s 50b03ded-24b4-41b0-a7f7-b6e516da8b7a -x SELECT -w 900

--------------------------------------------- ACTIVE SESSION HISTORY ---------------------------------------------------------------
SchemaName   Digest                           CountStar  WaitSecs   LockSecs  MaxWaitMs   RowsAffected       RowsSent   RowsExamined
------------ -------------------------------- --------- --------- ---------- ---------- -------------- -------------- --------------
mysql        452a11153b9c2af7e24bd248f56db657         1        65          0      65734              0           8805        4058162
***********  29d388a4d6674e2692e482d421a35738      4604        84         21       2778              0        5183291       20733267
***********  59dab324581513327af2263da8b14534      4680        46         19       2581              0        6903081        6903081
***********  245ff561530a55e970c39ea5e21c711b      4604       117         54       2193              0       15459268       30921081
***********  728df02c7b00146f4de8f344cdcef5a7        74         7          6       1803              0            639         187970
***********  6c8f5d994903e82df0a5154cca2d0f31      4601        81         33       1394              0       15481492       15481492
***********  858ea02df09243d4a25f954bccd7f9ce     41705        44         16       1022              0        2043834        3139791
***********  090b44c4be4ece566a6123810ad8d827     41703        42          8       1011              0        2509313        2747809
***********  e042f1f9ab6a71a38af28c57c5f27bc0    313540        95         39       1005              0         264542        1962198
***********  ffa76801561dcc750364fd32d8e0c1d1    123366        35         21        997              0         123356         123356
***********  55fa43672adbf985f8ed7aa3bd8f011b    177361       726        542        994              0         177363         532086
***********  902b98225c9edfc3267aff3e579bda16      6779        35         28        983              0           6589           6589
***********  29ae62b6c03f10a81f6bba5526527372     46890        28         20        976              0          46170          46170
***********  dd94a50813ccb9856b5eb0e65421caa1     42182        41         19        976              0          75132        2542644
***********  3f84d27deb0186997b26981bcc6c7f3e     43631       159        126        970              0          43556          55608
***********  148abefa2be2aa61398fc4b5bf764578    104928        56         41        969              0         104927         104928
***********  1e2b0da1a94fa57684bdea380769219b     46861       155        120        963              0          27540          27540
***********  290d8ec598416818e837b14c81514dc1      4735        46         22        956              0         281039        2016865
***********  056c5cc74ff2279b9cdb3125334360ac     56114        42         28        955              0          56076         112152
***********  43f3a17727631395b2765bdd6fae20f2      7559        41         33        953              0           7559         106721
***********  6167021589007fc64d1a2f9b41d0775d     47332         9          5        944              0          47279          47279
***********  a3d441eb3f1e941d487033ce2d5efa96    273576        91         63        927              0         273573         273577
***********  aaf3389222b54ff2cacdd671b60839bc     11320         3          2        924              0          11285          11285
***********  ad37ccd80bccaeea40151408fd5a0b66     32336        67         48        921              0         175609         175653
***********  1e09fd2167795bdb9a9ecf823441bc9a     12291        23         16        911              0          11640          34920
***********  25775934d8385b3d2766b36b25edb0b0     42182        13          7        908              0              0         127135
***********  154b932d87179356d249760a1ed290df    178014        51         28        902              0           6864          77420
***********  6fd6ed1765f65068ab133ea800d57dab    177186        66         36        900              0         166938         333882
```
all of the above queries had at least one instance when their latency was >= 900ms (recorded in MaxWaitMs column on the above report).  Mystery solved - we gave the list to the product engineering team and later determined that this was an insignificant change for our use-case.




## Conclusion

With careful planning, automation and rehearsals it's entirely possible to enable KMS Encryption At Rest on a large number of existing RDS Instances.  And there are a few options to choose from (online vs cold).  It would be a lot simpler if AWS RDS supported enabling KMS Encryption on existing instances out of the box, but as of Mar 23 2019 it didn't — at least not on our version of MySQL - 5.6.x.
