#!/bin/bash

cat ign_config.json | gzip -9 -c | base64 > compressed_and_encoded_ign_config.json.gz
cat ign_config.json | gzip -9 -c > compressed_ign_config.json.gz
