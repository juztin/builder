# Docker Image Builder

This is a work-in-progress, use at your own risk.


#### Examples

##### CLI

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

##### Jenkins


```groovy
@Library('pipeline-library') _

node {
    stage("Create Docker Imaage") {
        git branch: "master", credentialsId: "ghe_token", url: "https://ghe.coxautoinc.com/DMS/Docker.git"

        def dockerFiles = sh(
            returnStdout: true, 
            script: 'git diff --name-only $(git rev-parse HEAD^) $(git rev-parse HEAD) | { grep "Dockerfile" || true; } | paste -s -d, -')

        steps.withCredentials([[$class: 'UsernamePasswordMultiBinding', credentialsId: 'artifactory', usernameVariable: 'username', passwordVariable: 'password']]) {
            docker.image("minty/builder").inside {
                stage("Build & Push") {
                    sh """/bin/builder -username=$username -password=$password -files=$dockerFiles"""
                }
            }
        }
    }
}
```
