#!/usr/bin/env bats


setup() {
    load 'bats/bats-support/load'
    load 'bats/bats-assert/load'
    load 'bats/bats-file/load'

    DIR="$( cd "$( dirname "$BATS_TEST_FILENAME" )" >/dev/null 2>&1 && pwd )"
   
    cleanUpCmd=""
}

teardown() {
    run ${cleanUpCmd}
}

@test "Single NIC" {
    output_dir=${DIR}/_output/single-nic
    rm -rf -- "${output_dir}"
    mkdir -p "${output_dir}"

    run kcli create plan -f plans/single-nic.yml -P output_dir=${output_dir}
    cleanUpCmd="kcli delete -y vm vm3"

    output_file=${output_dir}/configure-ovs-output.txt
    assert_file_contains "${output_file}" "Brought up connection br-ex successfully"
    assert_file_contains "${output_file}" "Brought up connection ovs-if-br-ex successfully"
}

@test "Bonding NICs" {
    output_dir=${DIR}/_output/bonding-nics/
    rm -rf -- "${output_dir}"
    mkdir -p "${output_dir}"

    run kcli create plan -f plans/bonding-nics.yml -P output_dir=${output_dir}
    cleanUpCmd="kcli delete -y vm vm3"

    output_file=${output_dir}/configure-ovs-output.txt
    assert_file_contains "${output_file}" "Brought up connection br-ex successfully"
    assert_file_contains "${output_file}" "Brought up connection ovs-if-br-ex successfully"
    assert_file_contains "${output_file}" "convert_to_bridge bond99 br-ex phys0 48"
}
