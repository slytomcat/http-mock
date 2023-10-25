FROM scratch
ENV MANAGEMENT_HOST=localhost
ENV MANAGEMENT_PORT=8080
WORKDIR /opt/http-mock
COPY http-mock .
CMD ["./http-mock -s ${MANAGEMENT_HOST} -p ${MANAGEMENT_PORT}"]
