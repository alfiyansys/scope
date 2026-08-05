FROM golang:1.22-bookworm
RUN apt-get update && \
    apt-get install -y \
      curl \
      file \
      git \
      jq \
      libprotobuf-dev \
      make \
      protobuf-compiler \
      python3 \
      python3-pip \
      python3-requests \
      python3-yaml \
      libssl-dev \
      python3-openssl \
      shellcheck \
      unzip && \
    rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/*
RUN pip3 install --break-system-packages attrs==19.2.0 pyhcl yapf==0.16.2 flake8==3.3.0
RUN curl -fsSLo shfmt https://github.com/mvdan/sh/releases/download/v1.3.0/shfmt_v1.3.0_linux_amd64 && \
	echo "b1925c2c405458811f0c227266402cf1868b4de529f114722c2e3a5af4ac7bb2  shfmt" | sha256sum -c && \
	chmod +x shfmt && \
	mv shfmt /usr/bin
# gvt and golang/dep (pre-modules vendoring tools) are no longer needed: this
# project vendors via `go mod vendor` now (see MODERNIZATION-PLAN.md Phase 1).
RUN go install github.com/client9/misspell/cmd/misspell@latest && \
	go install github.com/fzipp/gocyclo/cmd/gocyclo@latest && \
	go install github.com/gogo/protobuf/protoc-gen-gogoslick@latest && \
	go install golang.org/x/lint/golint@latest && \
	go install github.com/golang/protobuf/protoc-gen-go@latest && \
	go install github.com/kisielk/errcheck@latest && \
	go install github.com/mjibson/esc@v0.2.0 && \
	rm -rf /go/pkg/mod/cache
RUN mkdir protoc && \
	cd protoc && \
	curl -O -L https://github.com/google/protobuf/releases/download/v3.1.0/protoc-3.1.0-linux-x86_64.zip && \
	unzip protoc-3.1.0-linux-x86_64.zip && \
	cp bin/protoc /usr/bin/ && \
	chmod o+x /usr/bin/protoc && \
	cd .. && \
	rm -rf protoc
RUN mkdir -p /var/run/secrets/kubernetes.io/serviceaccount && \
		touch /var/run/secrets/kubernetes.io/serviceaccount/token
COPY build.sh /
ENTRYPOINT ["/build.sh"]

ARG revision
LABEL maintainer="Weaveworks <help@weave.works>" \
      org.opencontainers.image.title="golang" \
      org.opencontainers.image.source="https://github.com/weaveworks/build-tools/tree/master/build/golang" \
      org.opencontainers.image.revision="${revision}" \
      org.opencontainers.image.vendor="Weaveworks"
