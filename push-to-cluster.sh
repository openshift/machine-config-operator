podman build -t quay.io/dkhater/testing:latest .

podman push --authfile  /Users/daliakhater/Downloads/dkhater-dkhater-workstation-auth.json quay.io/dkhater/testing:latest

mco-push replace quay.io/dkhater/testing:latest