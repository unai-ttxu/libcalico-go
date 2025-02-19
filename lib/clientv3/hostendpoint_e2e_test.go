// Copyright (c) 2017 Tigera, Inc. All rights reserved.

// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package clientv3_test

import (
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/ginkgo/extensions/table"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"context"

	"github.com/unai-ttxu/libcalico-go/lib/apiconfig"
	apiv3 "github.com/unai-ttxu/libcalico-go/lib/apis/v3"
	"github.com/unai-ttxu/libcalico-go/lib/backend"
	"github.com/unai-ttxu/libcalico-go/lib/clientv3"
	"github.com/unai-ttxu/libcalico-go/lib/numorstring"
	"github.com/unai-ttxu/libcalico-go/lib/options"
	"github.com/unai-ttxu/libcalico-go/lib/testutils"
	"github.com/unai-ttxu/libcalico-go/lib/watch"
)

var _ = testutils.E2eDatastoreDescribe("HostEndpoint tests", testutils.DatastoreEtcdV3, func(config apiconfig.CalicoAPIConfig) {

	ctx := context.Background()
	name1 := "hep-1"
	name2 := "hep-2"
	name3 := "hep-3"
	spec1 := apiv3.HostEndpointSpec{
		Node:          "node1",
		InterfaceName: "eth0",
		Ports: []apiv3.EndpointPort{
			{
				Port:     1234,
				Name:     "foobar",
				Protocol: numorstring.ProtocolFromString("TCP"),
			},
			{
				Port:     5432,
				Name:     "bop",
				Protocol: numorstring.ProtocolFromString("TCP"),
			},
		},
	}
	spec2 := apiv3.HostEndpointSpec{
		Node:          "node2",
		InterfaceName: "eth1",
		Ports: []apiv3.EndpointPort{
			{
				Port:     5678,
				Name:     "bazzbiff",
				Protocol: numorstring.ProtocolFromString("UDP"),
			},
		},
	}
	spec3 := apiv3.HostEndpointSpec{
		Node:          "node2",
		InterfaceName: "*",
	}

	DescribeTable("HostEndpoint e2e CRUD tests",
		func(name1, name2 string, spec1, spec2 apiv3.HostEndpointSpec) {
			c, err := clientv3.New(config)
			Expect(err).NotTo(HaveOccurred())

			be, err := backend.NewClient(config)
			Expect(err).NotTo(HaveOccurred())
			be.Clean()

			By("Updating the HostEndpoint before it is created")
			_, outError := c.HostEndpoints().Update(ctx, &apiv3.HostEndpoint{
				ObjectMeta: metav1.ObjectMeta{Name: name1, ResourceVersion: "1234", CreationTimestamp: metav1.Now(), UID: "test-fail-hostendpoint"},
				Spec:       spec1,
			}, options.SetOptions{})
			Expect(outError).To(HaveOccurred())
			Expect(outError.Error()).To(ContainSubstring("resource does not exist: HostEndpoint(" + name1 + ") with error:"))

			By("Attempting to creating a new HostEndpoint with name1/spec1 and a non-empty ResourceVersion")
			_, outError = c.HostEndpoints().Create(ctx, &apiv3.HostEndpoint{
				ObjectMeta: metav1.ObjectMeta{Name: name1, ResourceVersion: "12345"},
				Spec:       spec1,
			}, options.SetOptions{})
			Expect(outError).To(HaveOccurred())
			Expect(outError.Error()).To(Equal("error with field Metadata.ResourceVersion = '12345' (field must not be set for a Create request)"))

			By("Creating a new HostEndpoint with name1/spec1")
			res1, outError := c.HostEndpoints().Create(ctx, &apiv3.HostEndpoint{
				ObjectMeta: metav1.ObjectMeta{Name: name1},
				Spec:       spec1,
			}, options.SetOptions{})
			Expect(outError).NotTo(HaveOccurred())
			Expect(res1).To(MatchResource(apiv3.KindHostEndpoint, testutils.ExpectNoNamespace, name1, spec1))

			// Track the version of the original data for name1.
			rv1_1 := res1.ResourceVersion

			By("Attempting to create the same HostEndpoint with name1 but with spec2")
			_, outError = c.HostEndpoints().Create(ctx, &apiv3.HostEndpoint{
				ObjectMeta: metav1.ObjectMeta{Name: name1},
				Spec:       spec2,
			}, options.SetOptions{})
			Expect(outError).To(HaveOccurred())
			Expect(outError.Error()).To(Equal("resource already exists: HostEndpoint(" + name1 + ")"))

			By("Getting HostEndpoint (name1) and comparing the output against spec1")
			res, outError := c.HostEndpoints().Get(ctx, name1, options.GetOptions{})
			Expect(outError).NotTo(HaveOccurred())
			Expect(res).To(MatchResource(apiv3.KindHostEndpoint, testutils.ExpectNoNamespace, name1, spec1))
			Expect(res.ResourceVersion).To(Equal(res1.ResourceVersion))

			By("Getting HostEndpoint (name2) before it is created")
			_, outError = c.HostEndpoints().Get(ctx, name2, options.GetOptions{})
			Expect(outError).To(HaveOccurred())
			Expect(outError.Error()).To(ContainSubstring("resource does not exist: HostEndpoint(" + name2 + ") with error:"))

			By("Listing all the HostEndpoints, expecting a single result with name1/spec1")
			outList, outError := c.HostEndpoints().List(ctx, options.ListOptions{})
			Expect(outError).NotTo(HaveOccurred())
			Expect(outList.Items).To(ConsistOf(
				testutils.Resource(apiv3.KindHostEndpoint, testutils.ExpectNoNamespace, name1, spec1),
			))

			By("Creating a new HostEndpoint with name2/spec2")
			res2, outError := c.HostEndpoints().Create(ctx, &apiv3.HostEndpoint{
				ObjectMeta: metav1.ObjectMeta{Name: name2},
				Spec:       spec2,
			}, options.SetOptions{})
			Expect(outError).NotTo(HaveOccurred())
			Expect(res2).To(MatchResource(apiv3.KindHostEndpoint, testutils.ExpectNoNamespace, name2, spec2))

			By("Getting HostEndpoint (name2) and comparing the output against spec2")
			res, outError = c.HostEndpoints().Get(ctx, name2, options.GetOptions{})
			Expect(outError).NotTo(HaveOccurred())
			Expect(res2).To(MatchResource(apiv3.KindHostEndpoint, testutils.ExpectNoNamespace, name2, spec2))
			Expect(res.ResourceVersion).To(Equal(res2.ResourceVersion))

			By("Listing all the HostEndpoints, expecting a two results with name1/spec1 and name2/spec2")
			outList, outError = c.HostEndpoints().List(ctx, options.ListOptions{})
			Expect(outError).NotTo(HaveOccurred())
			Expect(outList.Items).To(ConsistOf(
				testutils.Resource(apiv3.KindHostEndpoint, testutils.ExpectNoNamespace, name1, spec1),
				testutils.Resource(apiv3.KindHostEndpoint, testutils.ExpectNoNamespace, name2, spec2),
			))
			By("Updating HostEndpoint name1 with spec2")
			res1.Spec = spec2
			res1, outError = c.HostEndpoints().Update(ctx, res1, options.SetOptions{})
			Expect(outError).NotTo(HaveOccurred())
			Expect(res1).To(MatchResource(apiv3.KindHostEndpoint, testutils.ExpectNoNamespace, name1, spec2))

			By("Attempting to update the HostEndpoint without a Creation Timestamp")
			res, outError = c.HostEndpoints().Update(ctx, &apiv3.HostEndpoint{
				ObjectMeta: metav1.ObjectMeta{Name: name1, ResourceVersion: "1234", UID: "test-fail-hostendpoint"},
				Spec:       spec1,
			}, options.SetOptions{})
			Expect(outError).To(HaveOccurred())
			Expect(res).To(BeNil())
			Expect(outError.Error()).To(Equal("error with field Metadata.CreationTimestamp = '0001-01-01 00:00:00 +0000 UTC' (field must be set for an Update request)"))

			By("Attempting to update the HostEndpoint without a UID")
			res, outError = c.HostEndpoints().Update(ctx, &apiv3.HostEndpoint{
				ObjectMeta: metav1.ObjectMeta{Name: name1, ResourceVersion: "1234", CreationTimestamp: metav1.Now()},
				Spec:       spec1,
			}, options.SetOptions{})
			Expect(outError).To(HaveOccurred())
			Expect(res).To(BeNil())
			Expect(outError.Error()).To(Equal("error with field Metadata.UID = '' (field must be set for an Update request)"))

			// Track the version of the updated name1 data.
			rv1_2 := res1.ResourceVersion

			By("Updating HostEndpoint name1 without specifying a resource version")
			res1.Spec = spec1
			res1.ObjectMeta.ResourceVersion = ""
			_, outError = c.HostEndpoints().Update(ctx, res1, options.SetOptions{})
			Expect(outError).To(HaveOccurred())
			Expect(outError.Error()).To(Equal("error with field Metadata.ResourceVersion = '' (field must be set for an Update request)"))

			By("Updating HostEndpoint name1 using the previous resource version")
			res1.Spec = spec1
			res1.ResourceVersion = rv1_1
			_, outError = c.HostEndpoints().Update(ctx, res1, options.SetOptions{})
			Expect(outError).To(HaveOccurred())
			Expect(outError.Error()).To(Equal("update conflict: HostEndpoint(" + name1 + ")"))

			By("Getting HostEndpoint (name1) with the original resource version and comparing the output against spec1")
			res, outError = c.HostEndpoints().Get(ctx, name1, options.GetOptions{ResourceVersion: rv1_1})
			Expect(outError).NotTo(HaveOccurred())
			Expect(res).To(MatchResource(apiv3.KindHostEndpoint, testutils.ExpectNoNamespace, name1, spec1))
			Expect(res.ResourceVersion).To(Equal(rv1_1))

			By("Getting HostEndpoint (name1) with the updated resource version and comparing the output against spec2")
			res, outError = c.HostEndpoints().Get(ctx, name1, options.GetOptions{ResourceVersion: rv1_2})
			Expect(outError).NotTo(HaveOccurred())
			Expect(res).To(MatchResource(apiv3.KindHostEndpoint, testutils.ExpectNoNamespace, name1, spec2))
			Expect(res.ResourceVersion).To(Equal(rv1_2))

			By("Listing HostEndpoints with the original resource version and checking for a single result with name1/spec1")
			outList, outError = c.HostEndpoints().List(ctx, options.ListOptions{ResourceVersion: rv1_1})
			Expect(outError).NotTo(HaveOccurred())
			Expect(outList.Items).To(ConsistOf(
				testutils.Resource(apiv3.KindHostEndpoint, testutils.ExpectNoNamespace, name1, spec1),
			))

			By("Listing HostEndpoints with the latest resource version and checking for two results with name1/spec2 and name2/spec2")
			outList, outError = c.HostEndpoints().List(ctx, options.ListOptions{})
			Expect(outError).NotTo(HaveOccurred())
			Expect(outList.Items).To(ConsistOf(
				testutils.Resource(apiv3.KindHostEndpoint, testutils.ExpectNoNamespace, name1, spec2),
				testutils.Resource(apiv3.KindHostEndpoint, testutils.ExpectNoNamespace, name2, spec2),
			))

			By("Deleting HostEndpoint (name1) with the old resource version")
			_, outError = c.HostEndpoints().Delete(ctx, name1, options.DeleteOptions{ResourceVersion: rv1_1})
			Expect(outError).To(HaveOccurred())
			Expect(outError.Error()).To(Equal("update conflict: HostEndpoint(" + name1 + ")"))

			By("Deleting HostEndpoint (name1) with the new resource version")
			dres, outError := c.HostEndpoints().Delete(ctx, name1, options.DeleteOptions{ResourceVersion: rv1_2})
			Expect(outError).NotTo(HaveOccurred())
			Expect(dres).To(MatchResource(apiv3.KindHostEndpoint, testutils.ExpectNoNamespace, name1, spec2))

			By("Updating HostEndpoint name2 with a 2s TTL and waiting for the entry to be deleted")
			_, outError = c.HostEndpoints().Update(ctx, res2, options.SetOptions{TTL: 2 * time.Second})
			Expect(outError).NotTo(HaveOccurred())
			time.Sleep(1 * time.Second)
			_, outError = c.HostEndpoints().Get(ctx, name2, options.GetOptions{})
			Expect(outError).NotTo(HaveOccurred())
			time.Sleep(2 * time.Second)
			_, outError = c.HostEndpoints().Get(ctx, name2, options.GetOptions{})
			Expect(outError).To(HaveOccurred())
			Expect(outError.Error()).To(ContainSubstring("resource does not exist: HostEndpoint(" + name2 + ") with error:"))

			By("Creating HostEndpoint name2 with a 2s TTL and waiting for the entry to be deleted")
			_, outError = c.HostEndpoints().Create(ctx, &apiv3.HostEndpoint{
				ObjectMeta: metav1.ObjectMeta{Name: name2},
				Spec:       spec2,
			}, options.SetOptions{TTL: 2 * time.Second})
			Expect(outError).NotTo(HaveOccurred())
			time.Sleep(1 * time.Second)
			_, outError = c.HostEndpoints().Get(ctx, name2, options.GetOptions{})
			Expect(outError).NotTo(HaveOccurred())
			time.Sleep(2 * time.Second)
			_, outError = c.HostEndpoints().Get(ctx, name2, options.GetOptions{})
			Expect(outError).To(HaveOccurred())
			Expect(outError.Error()).To(ContainSubstring("resource does not exist: HostEndpoint(" + name2 + ") with error:"))

			By("Attempting to deleting HostEndpoint (name2) again")
			_, outError = c.HostEndpoints().Delete(ctx, name2, options.DeleteOptions{})
			Expect(outError).To(HaveOccurred())
			Expect(outError.Error()).To(ContainSubstring("resource does not exist: HostEndpoint(" + name2 + ") with error:"))

			By("Listing all HostEndpoints and expecting no items")
			outList, outError = c.HostEndpoints().List(ctx, options.ListOptions{})
			Expect(outError).NotTo(HaveOccurred())
			Expect(outList.Items).To(HaveLen(0))

			By("Getting HostEndpoint (name2) and expecting an error")
			_, outError = c.HostEndpoints().Get(ctx, name2, options.GetOptions{})
			Expect(outError).To(HaveOccurred())
			Expect(outError.Error()).To(ContainSubstring("resource does not exist: HostEndpoint(" + name2 + ") with error:"))
		},

		// Test 1: Pass two fully populated HostEndpointSpecs and expect the series of operations to succeed.
		Entry("Two fully populated HostEndpointSpecs", name1, name2, spec1, spec2),
		Entry("Two fully populated HostEndpointSpecs", name2, name3, spec2, spec3),
		Entry("Two fully populated HostEndpointSpecs", name3, name1, spec3, spec1),
	)

	Describe("HostEndpoint watch functionality", func() {
		It("should handle watch events for different resource versions and event types", func() {
			c, err := clientv3.New(config)
			Expect(err).NotTo(HaveOccurred())

			be, err := backend.NewClient(config)
			Expect(err).NotTo(HaveOccurred())
			be.Clean()

			By("Listing HostEndpoints with the latest resource version and checking for two results with name1/spec2 and name2/spec2")
			outList, outError := c.HostEndpoints().List(ctx, options.ListOptions{})
			Expect(outError).NotTo(HaveOccurred())
			Expect(outList.Items).To(HaveLen(0))
			rev0 := outList.ResourceVersion

			By("Configuring a HostEndpoint name1/spec1 and storing the response")
			outRes1, err := c.HostEndpoints().Create(
				ctx,
				&apiv3.HostEndpoint{
					ObjectMeta: metav1.ObjectMeta{Name: name1},
					Spec:       spec1,
				},
				options.SetOptions{},
			)
			rev1 := outRes1.ResourceVersion

			By("Configuring a HostEndpoint name2/spec2 and storing the response")
			outRes2, err := c.HostEndpoints().Create(
				ctx,
				&apiv3.HostEndpoint{
					ObjectMeta: metav1.ObjectMeta{Name: name2},
					Spec:       spec2,
				},
				options.SetOptions{},
			)

			By("Starting a watcher from revision rev1 - this should skip the first creation")
			w, err := c.HostEndpoints().Watch(ctx, options.ListOptions{ResourceVersion: rev1})
			Expect(err).NotTo(HaveOccurred())
			testWatcher1 := testutils.NewTestResourceWatch(config.Spec.DatastoreType, w)
			defer testWatcher1.Stop()

			By("Deleting res1")
			_, err = c.HostEndpoints().Delete(ctx, name1, options.DeleteOptions{})
			Expect(err).NotTo(HaveOccurred())

			By("Checking for two events, create res2 and delete re1")
			testWatcher1.ExpectEvents(apiv3.KindHostEndpoint, []watch.Event{
				{
					Type:   watch.Added,
					Object: outRes2,
				},
				{
					Type:     watch.Deleted,
					Previous: outRes1,
				},
			})
			testWatcher1.Stop()

			By("Starting a watcher from rev0 - this should get all events")
			w, err = c.HostEndpoints().Watch(ctx, options.ListOptions{ResourceVersion: rev0})
			Expect(err).NotTo(HaveOccurred())
			testWatcher2 := testutils.NewTestResourceWatch(config.Spec.DatastoreType, w)
			defer testWatcher2.Stop()

			By("Modifying res2")
			outRes3, err := c.HostEndpoints().Update(
				ctx,
				&apiv3.HostEndpoint{
					ObjectMeta: outRes2.ObjectMeta,
					Spec:       spec1,
				},
				options.SetOptions{},
			)
			Expect(err).NotTo(HaveOccurred())
			testWatcher2.ExpectEvents(apiv3.KindHostEndpoint, []watch.Event{
				{
					Type:   watch.Added,
					Object: outRes1,
				},
				{
					Type:   watch.Added,
					Object: outRes2,
				},
				{
					Type:     watch.Deleted,
					Previous: outRes1,
				},
				{
					Type:     watch.Modified,
					Previous: outRes2,
					Object:   outRes3,
				},
			})
			testWatcher2.Stop()

			// Only etcdv3 supports watching a specific instance of a resource.
			if config.Spec.DatastoreType == apiconfig.EtcdV3 {
				By("Starting a watcher from rev0 watching name1 - this should get all events for name1")
				w, err = c.HostEndpoints().Watch(ctx, options.ListOptions{Name: name1, ResourceVersion: rev0})
				Expect(err).NotTo(HaveOccurred())
				testWatcher2_1 := testutils.NewTestResourceWatch(config.Spec.DatastoreType, w)
				defer testWatcher2_1.Stop()
				testWatcher2_1.ExpectEvents(apiv3.KindHostEndpoint, []watch.Event{
					{
						Type:   watch.Added,
						Object: outRes1,
					},
					{
						Type:     watch.Deleted,
						Previous: outRes1,
					},
				})
				testWatcher2_1.Stop()
			}

			By("Starting a watcher not specifying a rev - expect the current snapshot")
			w, err = c.HostEndpoints().Watch(ctx, options.ListOptions{})
			Expect(err).NotTo(HaveOccurred())
			testWatcher3 := testutils.NewTestResourceWatch(config.Spec.DatastoreType, w)
			defer testWatcher3.Stop()
			testWatcher3.ExpectEvents(apiv3.KindHostEndpoint, []watch.Event{
				{
					Type:   watch.Added,
					Object: outRes3,
				},
			})
			testWatcher3.Stop()

			By("Configuring HostEndpoint name1/spec1 again and storing the response")
			outRes1, err = c.HostEndpoints().Create(
				ctx,
				&apiv3.HostEndpoint{
					ObjectMeta: metav1.ObjectMeta{Name: name1},
					Spec:       spec1,
				},
				options.SetOptions{},
			)

			By("Starting a watcher not specifying a rev - expect the current snapshot")
			w, err = c.HostEndpoints().Watch(ctx, options.ListOptions{})
			Expect(err).NotTo(HaveOccurred())
			testWatcher4 := testutils.NewTestResourceWatch(config.Spec.DatastoreType, w)
			defer testWatcher4.Stop()
			testWatcher4.ExpectEventsAnyOrder(apiv3.KindHostEndpoint, []watch.Event{
				{
					Type:   watch.Added,
					Object: outRes1,
				},
				{
					Type:   watch.Added,
					Object: outRes3,
				},
			})

			By("Cleaning the datastore and expecting deletion events for each configured resource (tests prefix deletes results in individual events for each key)")
			be.Clean()
			testWatcher4.ExpectEvents(apiv3.KindHostEndpoint, []watch.Event{
				{
					Type:     watch.Deleted,
					Previous: outRes1,
				},
				{
					Type:     watch.Deleted,
					Previous: outRes3,
				},
			})
			testWatcher4.Stop()
		})
	})
})
