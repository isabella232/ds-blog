import (
	"fmt"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/rds"
	"github.com/davecgh/go-spew/spew"
)

// RestoreInstance - restore RDS snapshot for `sorceInstance` and match all of it's configurations
// waits for DBParameterGroupName to take effect before returning to caller (does a reboot as a final step)
func (s *SDK) RestoreInstance(sorceInstance Instance, takeFreshSnap bool, kmsKeyID string, np *NameParser) (Instance, error) {
	targetName := np.NewName(sorceInstance.Name)
	dbParGroupName := sorceInstance.RDSDBInstance.DBParameterGroups[0].DBParameterGroupName
	vpcSecurityGroups := sorceInstance.FilterVPCSecurityGroups(Active)

	// check if the targetName already exists and if so return it
	i, err := s.Describe(targetName)
	if err != nil {
		if !AWSError(err, rds.ErrCodeDBInstanceNotFoundFault) {
			return Instance{}, err
		}
	}

	if i.Name == targetName {
		s.log.Printf("... RestoreInstance: [%24s] target name %q already exists with status %q", sorceInstance.Name, targetName, i.Status)
		return s.ModifyInstance(targetName, *dbParGroupName, vpcSecurityGroups)
	}

	snap, err := s.GenerateSnapshot(
		sorceInstance.RDSDBInstance.DBInstanceIdentifier,
		takeFreshSnap,
		kmsKeyID)
	if err != nil {
		return Instance{}, err
	}
	if s.Verbose {
		spew.Dump(snap)
	}

	s.log.Printf("... RestoreInstance: [%24s] Restoring from %q to %q", sorceInstance.Name, *snap.DBSnapshotIdentifier, targetName)
	snapInput := &rds.RestoreDBInstanceFromDBSnapshotInput{
		AutoMinorVersionUpgrade: sorceInstance.RDSDBInstance.AutoMinorVersionUpgrade,
		// AvailabilityZone:            sorceInstance.RDSDBInstance.AvailabilityZone,
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
}
