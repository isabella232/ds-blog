# Enable RDS Encryption At Rest For Existing Instance
To enable Encryption At Rest on an **existing** RDS MySQL instance, there are a number of important details you have to get right to make it work — this blog post is about these details.

Before delving into these details, lets dispel some uncertainty:

Why can't you simply enable Encryption At Rest on an existing instance by fliping the switch to "ON"?  Here's what AWS documetation has to say about that:

> You can only enable encryption for an Amazon RDS DB instance when you create it, not after the DB instance is created.
>
> However, because you can encrypt a copy of an unencrypted DB snapshot, you can effectively add encryption to an unencrypted DB instance. That is, you can create a snapshot of your DB instance, and then create an encrypted copy of that snapshot. You can then restore a DB instance from the encrypted snapshot, and thus you have an encrypted copy of your original DB instance.

REF: [Encryption.Limitations](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/Overview.Encryption.html#Overview.Encryption.Limitations)

You also cannot create an encrypted Read Replica from an unencrypted DB instance:

> You cannot create an encrypted Read Replica from an unencrypted DB instance. (Service: AmazonRDS; Status Code:
>
>  400; Error Code: InvalidParameterCombination; Request ID: 943905dc-116c-4f7b-9963-e0c5766fd92f)

![](cannot-create-an-encrypted-Read-Replica.png)

So what's the solution then?  There are at least two methods to enable Encryption At Rest for an existing RDS instance:

1. **Limited Downtime**: encrypting and promoting a read replica
2. **Complete Downtime**: cold backup / restore approach

We picked **cold backup / restore approach** due to it's lower complexity, limited time and the shear volume of the instances we had to enable Encryption At Rest.  However, both methods share the same important step: *restoring an RDS Instance from an encrypted snapshot*.  The only difference is that method 1) uses the snapshot of the read-replica and method 2) snapshot of the master/primary itself.

And there are some important gaps in the [AWS RDS API RestoreDBInstanceFromDBSnapshot](https://docs.aws.amazon.com/AmazonRDS/latest/APIReference/API_RestoreDBInstanceFromDBSnapshot.html) that have to be filled with proper automation, and we'll delve into them in the following section.  But before going there, lets go over the high level process overview of the **cold backup / restore approach** we went through.

## Cold Backup / Restore Approach

1. Enable site wide maintenance
2. Shutdown all services
3. Shutdown analytics processes
4. Rename the databases with `old-` prefix
5. Bounce the databases to clear any connections
6. Verify no connections exist
7. Take an RDS snapshot across all databases
8. Encrypt snapshots
9. Restore encrypted snapshots with `new-` prefix
10. Re-create replicas and analytics users and setup
11. Rename all newly encrypted instances and their replicas back to their original names
12. Restart all services
13. Restart/validate analytics processes
14. Validate functionality
15. Disable site wide maintenance

The interesting bits are in steps 9 and 10 and that's what we'll focus on next.

## Restore Encrypted Snapshots

There are some important gaps in the [AWS RDS API RestoreDBInstanceFromDBSnapshot](https://docs.aws.amazon.com/AmazonRDS/latest/APIReference/API_RestoreDBInstanceFromDBSnapshot.html) that have to be filled with proper automation.  Our infrustructure automation language of choice is [Go](https://golang.org/) and we'll use the real snippets of code from our tooling to go over these gaps.