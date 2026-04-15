ARG BASE_IMAGE=public.ecr.aws/eks-distro-build-tooling/eks-distro-minimal-base:latest
FROM ${BASE_IMAGE}

ARG TARGETOS=linux
ARG TARGETARCH=amd64

COPY bin/${TARGETOS}/${TARGETARCH}/gateway /gateway

ENTRYPOINT ["/gateway"]
