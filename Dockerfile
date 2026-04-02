FROM public.ecr.aws/eks-distro-build-tooling/eks-distro-minimal-base:latest

ARG TARGETOS=linux
ARG TARGETARCH=amd64

COPY bin/${TARGETOS}/${TARGETARCH}/gateway /gateway

ENTRYPOINT ["/gateway"]
