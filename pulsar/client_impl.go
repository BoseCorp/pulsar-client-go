// Licensed to the Apache Software Foundation (ASF) under one
// or more contributor license agreements.  See the NOTICE file
// distributed with this work for additional information
// regarding copyright ownership.  The ASF licenses this file
// to you under the Apache License, Version 2.0 (the
// "License"); you may not use this file except in compliance
// with the License.  You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package pulsar

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/golang/protobuf/proto"

	log "github.com/sirupsen/logrus"

	"github.com/apache/pulsar-client-go/pkg/auth"
	"github.com/apache/pulsar-client-go/pkg/pb"
	"github.com/apache/pulsar-client-go/pulsar/internal"
)

type client struct {
	options ClientOptions

	cnxPool       internal.ConnectionPool
	rpcClient     internal.RPCClient
	lookupService internal.LookupService
	auth          auth.Provider

	handlers            internal.ClientHandlers
	producerIDGenerator uint64
	consumerIDGenerator uint64
}

func newClient(options ClientOptions) (Client, error) {
	if options.URL == "" {
		return nil, newError(ResultInvalidConfiguration, "URL is required for client")
	}

	url, err := url.Parse(options.URL)
	if err != nil {
		log.WithError(err).Error("Failed to parse service URL")
		return nil, newError(ResultInvalidConfiguration, "Invalid service URL")
	}

	var tlsConfig *internal.TLSOptions
	switch url.Scheme {
	case "pulsar":
		tlsConfig = nil
	case "pulsar+ssl":
		tlsConfig = &internal.TLSOptions{
			AllowInsecureConnection: options.TLSAllowInsecureConnection,
			TrustCertsFilePath:      options.TLSTrustCertsFilePath,
			ValidateHostname:        options.TLSValidateHostname,
		}
	default:
		return nil, newError(ResultInvalidConfiguration, fmt.Sprintf("Invalid URL scheme '%s'", url.Scheme))
	}

	var authProvider auth.Provider
	var ok bool

	if options.Authentication == nil {
		authProvider = auth.NewAuthDisabled()
	} else {
		authProvider, ok = options.Authentication.(auth.Provider)
		if !ok {
			return nil, errors.New("invalid auth provider interface")
		}
	}

	c := &client{
		cnxPool: internal.NewConnectionPool(tlsConfig, authProvider),
	}
	c.rpcClient = internal.NewRPCClient(url, c.cnxPool)
	c.lookupService = internal.NewLookupService(c.rpcClient, url)
	c.handlers = internal.NewClientHandlers()
	return c, nil
}

func (c *client) CreateProducer(options ProducerOptions) (Producer, error) {
	producer, err := newProducer(c, &options)
	if err == nil {
		c.handlers.Add(producer)
	}
	return producer, err
}

func (c *client) Subscribe(options ConsumerOptions) (Consumer, error) {
	consumer, err := newConsumer(c, options)
	if err != nil {
		return nil, err
	}
	c.handlers.Add(consumer)
	return consumer, nil
}

func (c *client) CreateReader(options ReaderOptions) (Reader, error) {
	// TODO: Implement reader
	return nil, nil
}

func (c *client) TopicPartitions(topic string) ([]string, error) {
	topicName, err := internal.ParseTopicName(topic)
	if err != nil {
		return nil, err
	}

	id := c.rpcClient.NewRequestID()
	res, err := c.rpcClient.RequestToAnyBroker(id, pb.BaseCommand_PARTITIONED_METADATA,
		&pb.CommandPartitionedTopicMetadata{
			RequestId: &id,
			Topic:     &topicName.Name,
		})
	if err != nil {
		return nil, err
	}

	r := res.Response.PartitionMetadataResponse
	if r.Error != nil {
		return nil, newError(ResultLookupError, r.GetError().String())
	}

	if r.GetPartitions() > 0 {
		partitions := make([]string, r.GetPartitions())
		for i := 0; i < int(r.GetPartitions()); i++ {
			partitions[i] = fmt.Sprintf("%s-partition-%d", topic, i)
		}
		return partitions, nil
	}
	// Non-partitioned topic
	return []string{topicName.Name}, nil
}

func (c *client) Close() {
	c.handlers.Close()
}

func (c *client) namespaceTopics(namespace string) ([]string, error) {
	id := c.rpcClient.NewRequestID()
	req := &pb.CommandGetTopicsOfNamespace{
		RequestId: proto.Uint64(id),
		Namespace: proto.String(namespace),
		Mode:      pb.CommandGetTopicsOfNamespace_PERSISTENT.Enum(),
	}
	res, err := c.rpcClient.RequestToAnyBroker(id, pb.BaseCommand_GET_TOPICS_OF_NAMESPACE, req)
	if err != nil {
		return nil, err
	}
	if res.Response.Error != nil {
		return []string{}, newError(ResultLookupError, res.Response.GetError().String())
	}

	return res.Response.GetTopicsOfNamespaceResponse.GetTopics(), nil
}
