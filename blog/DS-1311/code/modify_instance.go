// ModifyInstance - change RDS Instance DB Parameter Group and Vpc Security Group
// and wait for reboot of the instance for the change to take effect ...
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
	// set to the right value so we are not even checking it here, and if you are
	// smart enough to do this in two stages and bypass this "assumption" you should
	// know how to fix this yourself later ...
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
