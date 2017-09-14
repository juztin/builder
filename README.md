# Docker Image Builder

This is a work-in-progress, use at your own risk.


##### Examples

Build a single Dockerfile

```bash
docker run \
	-it \
	--rm \
	--privileged \
	--volume /var/run/docker.sock:/var/run/docker.sock \
	--volume "$(pwd)":/context \
	minty/builder -files=/context/Dockerfile -username=sam -password=s3cret
```
