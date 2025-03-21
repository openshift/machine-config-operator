#!/bin/bash

ORG="openshift"
REPO="openshift-tests-private"

# Define the Git repository URL
REPO_URL="https://github.com/$ORG/$REPO.git"

# Define the directory names for HTTPS and SSH clones
SSH_DIR="${REPO}_ssh"
HTTPS_DIR="${REPO}_https"

# Try cloning via SSH
echo "Attempting to clone via SSH..."
SSH_REPO_URL="git@github.com:$ORG/$REPO.git"
timeout 5m git clone --depth=1 "$SSH_REPO_URL" "$SSH_DIR"
SSH_RESULT=$?

# If SSH clone fails, print message
if [ $SSH_RESULT -ne 0 ]; then
  echo "SSH clone failed."
else
  echo "SSH clone successful."
fi

# Try cloning via HTTPS
echo "Attempting to clone via HTTPS..."
timeout 5m git clone --depth=1 "$REPO_URL" "$HTTPS_DIR"
HTTPS_RESULT=$?

# If HTTPS clone fails, print message and proceed to SSH
if [ $HTTPS_RESULT -ne 0 ]; then
  echo "HTTPS clone failed."
else
  echo "HTTPS clone successful."
fi

# Check the results and determine final success/failure
if [ $HTTPS_RESULT -ne 0 ] && [ $SSH_RESULT -ne 0 ]; then
  echo "Both HTTPS and SSH clone attempts failed. Exiting with failure."
  exit 1
else
  echo "At least one clone succeeded. Exiting with success."
  exit 0
fi
