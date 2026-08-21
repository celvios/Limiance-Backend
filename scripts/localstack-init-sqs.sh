#!/bin/sh
set -eu

awslocal sqs create-queue \
  --queue-name limiance-outbox \
  --attributes 'ReceiveMessageWaitTimeSeconds=20,VisibilityTimeout=60,MessageRetentionPeriod=1209600'

awslocal sqs create-queue \
  --queue-name limiance-notifications \
  --attributes 'ReceiveMessageWaitTimeSeconds=20,VisibilityTimeout=60,MessageRetentionPeriod=1209600'

awslocal sqs create-queue \
  --queue-name limiance-unrouted \
  --attributes 'ReceiveMessageWaitTimeSeconds=20,VisibilityTimeout=60,MessageRetentionPeriod=1209600'
