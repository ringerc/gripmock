// Execute with:
//    k6 -q run k6_test_simple.js

import { check, sleep } from 'k6';
import { describe, expect } from 'https://jslib.k6.io/k6chaijs/4.3.4.2/index.js';
import grpc from 'k6/net/grpc';

const suite = {
  name: "simple.proto",
  proto: {
    paths: ["."],
    file: "simple.proto",
  },
  tests: [
    {
      name: "simple",
      method: "simple.Gripmock/SayHello",
      data: { name: 'tokopedia' },
      expected: {
        message: "Hello Tokopedia",
        returnCode: 1,
      },
      status: grpc.StatusOK,
    },
    {
      name: "simple1",
      method: "simple.Gripmock/SayHello",
      data: { name: 'world' },
      expected: {
        message: "Hello World",
        returnCode: 1,
      },
      status: grpc.StatusOK,
    },
    {
      name: "missingstub",
      method: "simple.Gripmock/SayHello",
      data: { name: '' },
      expected: {"message":"","returnCode":0},
      status: grpc.StatusUnknown,
      err: (response) => { 
        expect(response.error.message, 'reply error message').to.satisfy(e => e.startsWith("Can't find stub"));
        expect(response.error.code, 'reply error code').to.equal(2);
      },
    },
  ],
};

const client = new grpc.Client();
client.load(suite.proto.paths, suite.proto.file);

export default () => {

  client.connect('localhost:4480', {
     plaintext: true
  });

  describe(suite.name||suite.proto.file, () => {
    for (const t of suite.tests) {
      describe(t.name, () => {
        console.log("running ", t)
        const response = client.invoke(t.method, t.data);
        console.log(JSON.stringify(response));
        expect(response.status, 'response status').to.equal(t.status);
        if (t.expected) {
          expect(response.message, 'reply message').to.deep.equal(t.expected)
        }
        if (t.err) {
          t.err(response)
        }
      });
    }
  });
 
  client.close();
};

// vim: ts=2 sw=2 et ai
