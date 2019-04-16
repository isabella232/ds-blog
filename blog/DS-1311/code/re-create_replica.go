import (
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/rds"
)

func (s *SDK) reCreateReplica(master, copyFrom Instance, name string, binlogRetention int, rootPass string) error {
	for _, replica := range master.RDSDBInstance.ReadReplicaDBInstanceIdentifiers {
		if *replica == name {
			s.log.Printf("... reCreateReplica: [%24s] %q replica already exists", master.Name, name)
			return s.reCreateReplicaFinalize(master, copyFrom, name, binlogRetention, rootPass)
		}
	}

	replicaInput := &rds.CreateDBInstanceReadReplicaInput{
		AutoMinorVersionUpgrade: copyFrom.RDSDBInstance.AutoMinorVersionUpgrade,
		// AvailabilityZone:            copyFrom.RDSDBInstance.AvailabilityZone,
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
		// Port:                        copyFrom.RDSDBInstance.DbInstancePort,
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

func (s *SDK) reCreateReplicaFinalize(master, copyFrom Instance, name string, binlogRetention int, rootPass string) error {
	groupName := copyFrom.RDSDBInstance.DBParameterGroups[0].DBParameterGroupName
	newReplica, err := s.ModifyInstance(name, *groupName, copyFrom.FilterVPCSecurityGroups(Active))
	if err != nil {
		return err
	}
	return s.cloneReplica(master, copyFrom, newReplica, binlogRetention, rootPass)
}

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

