#!/bin/bash
function go_mod_tidy {

    count=0
    while [ $count -lt 100 ]; do
        ret_go_mod=0
        go mod tidy || ret_go_mod=$?
        if [ $ret_go_mod -eq 0 ]; then
            break
        fi
        sleep 3
        echo "========try next to get lib========"
        ((count++))
    done
    sed -i'' -e '/^toolchain go/d' go.mod && rm -f go.mod-e

    if [ $count -eq 100 ]; then
        echo "can not download lib"
        echo "1" > /tmp/makebinary_result
        exit 1
    else
        echo "success to download lib"
    fi
}
go_mod_tidy
