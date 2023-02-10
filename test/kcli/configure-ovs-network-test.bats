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
    output_dir="${DIR}/_output/single-nic"
    rm -rf -- "${output_dir}"
    mkdir -p "${output_dir}"

    run kcli create plan -f plans/single-nic.yml -P output_dir="${output_dir}"
    cleanUpCmd="kcli delete -y vm vm3"

    output_file="${output_dir}/configure-ovs-output.txt"
    assert_file_contains "${output_file}" "Brought up connection br-ex successfully"
    assert_file_contains "${output_file}" "Brought up connection ovs-if-br-ex successfully"

    nmstate_file="${output_dir}/nmstate.txt"
    assert_default_route_interface ${nmstate_file} "br-ex"
    assert_brex_ip_matches ${nmstate_file} 192.168.122.*
}

@test "Bonding NICs" {
    output_dir="${DIR}/_output/bonding-nics/"
    rm -rf -- "${output_dir}"
    mkdir -p "${output_dir}"

    run kcli create plan -f plans/bonding-nics.yml -P output_dir="${output_dir}"
    cleanUpCmd="kcli delete -y vm vm3"

    output_file="${output_dir}/configure-ovs-output.txt"
    assert_file_contains "${output_file}" "Brought up connection br-ex successfully"
    assert_file_contains "${output_file}" "Brought up connection ovs-if-br-ex successfully"
    assert_file_contains "${output_file}" "convert_to_bridge bond99 br-ex phys0 48"

    nmstate_file="${output_dir}/nmstate.txt"
    assert_default_route_interface ${nmstate_file} "br-ex"
    assert_brex_ip_matches ${nmstate_file} 192.168.122.*
}


assert_brex_ip_matches() {
    local -r nmstate_file="$1"
    local -r regex=$2

    assert_nmstate_expression ${nmstate_file} \
        '.interfaces[] | select(.name=="br-ex" and .type=="ovs-interface") | .ipv4.address[0].ip' \
        ${regex}
}

assert_default_route_interface() {
    local -r nmstate_file="$1"
    local -r expected_interface="$2"
    assert_nmstate_expression ${nmstate_file} \
        '.routes.running | map(select(.destination=="0.0.0.0/0")) | sort_by(.metric)[0]."next-hop-interface"' \
        "${expected_interface}"
}

assert_nmstate_expression() {
    local -r nmstate_file="$1"
    local -r yq_expression="$2"
    local -r expected_regex=$3
    actual_output=`yq -r "$yq_expression" $nmstate_file`

    if [[ "$actual_output" =~ $expected_regex ]]; then
        return
    fi

    cat $nmstate_file \
    | batslib_decorate "NMState expression [$yq_expression] output [$actual_output] didn't match [$expected_regex]" \
    | fail
}
