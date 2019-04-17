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
