import { Button } from "@/src/components/ui/button";
import { Card } from "@/src/components/ui/card";
import { CreditCard, Plus } from "lucide-react";
import { api } from "@/src/utils/api";
import { useQueryOrganization } from "@/src/features/organizations/hooks";
import { stripeProducts } from "@/src/ee/features/billing/utils/stripeProducts";
import { useRouter } from "next/router";

export const PaymentManagement = () => {
  const router = useRouter();
  const organization = useQueryOrganization();

  // Fetch subscription data
  const { data: subscription } = api.cloudBilling.getSubscription.useQuery(
    {
      orgId: organization?.id ?? "",
    },
    {
      enabled: organization !== undefined,
    }
  );

  // Fetch usage data
  const { data: usage } = api.cloudBilling.getUsage.useQuery(
    {
      orgId: organization?.id ?? "",
    },
    {
      enabled: organization !== undefined,
    }
  );

  // Fetch organization details for credits
  const { data: orgDetails } = api.organizations.getDetails.useQuery(
    { orgId: organization?.id ?? "" },
    { enabled: !!organization }
  );

  // Fetch subscription history
  const { data: subscriptionHistory } = api.cloudBilling.getSubscriptionHistory.useQuery(
    {
      orgId: organization?.id ?? "",
      limit: 2, // Only fetch 2 recent invoices for the display
    },
    {
      enabled: organization !== undefined,
    }
  );

  // Mutation for creating checkout session
  const createCheckoutSession = api.cloudBilling.createStripeCheckoutSession.useMutation();

  // Update this to use useQuery
  const { data: customerPortalUrl } = api.cloudBilling.getStripeCustomerPortalUrl.useQuery(
    { orgId: organization?.id ?? "" },
    { enabled: !!organization }
  );

  // Add this near your other hooks
  const cancelSubscription = api.cloudBilling.cancelStripeSubscription.useMutation();

  const handleAddCredits = async () => {
    const creditsProduct = stripeProducts.find(p => p.id === "credits-plan");
    if (!creditsProduct) {
      console.error("Credits product not found");
      return;
    }

    const result = await createCheckoutSession.mutateAsync({
      orgId: organization?.id ?? "",
      stripeProductId: creditsProduct.stripeProductId,
    });

    if (result.url) window.location.href = result.url;
  };

  // Update the handler to use the query result
  const handleCustomerPortal = () => {
    if (customerPortalUrl) window.location.href = customerPortalUrl;
  };

  const currentPlan = subscription?.plan?.name || "Free Plan";
  const availableCredits = orgDetails?.credits || 0;
  const nextBillingDate = subscription?.current_period_end 
    ? new Date(subscription.current_period_end).toLocaleDateString()
    : "N/A";

  return (
    <div className="space-y-6">
      {/* Current Plan Section */}
      <Card className="p-6">
        <div className="flex items-center justify-between">
          <div>
            <h3 className="text-lg font-medium">Current Plan</h3>
            <h2 className="mt-2 text-2xl font-bold">{currentPlan}</h2>
            <p className="text-sm text-muted-foreground">
              ${subscription?.price?.amount || 0}/month
            </p>
          </div>
          <Button 
            variant="outline"
            onClick={() => router.push("/pricing")}
          >
            Upgrade Plan
          </Button>
        </div>
        <div className="mt-4 flex items-center justify-between border-t pt-4">
          <p className="text-sm text-muted-foreground">
            Next billing date: {nextBillingDate}
          </p>
          {subscription && (
            <Button
              variant="ghost"
              className="text-red-500 hover:bg-red-50 hover:text-red-600"
              onClick={() => {
                if (subscription?.plan?.id) {
                  cancelSubscription.mutate({
                    orgId: organization?.id ?? "",
                    stripeProductId: subscription.plan.id
                  });
                }
              }}
            >
              Cancel Subscription
            </Button>
          )}
        </div>
      </Card>

      {/* Credit Balance Section */}
      <Card className="p-6">
        <div className="flex items-center justify-between">
          <h3 className="text-lg font-medium">Credit Balance</h3>
          <Button onClick={handleAddCredits}>
            <Plus className="mr-2 h-4 w-4" />
            Add Credits
          </Button>
        </div>
        <div className="mt-4 flex items-center gap-3">
          <div className="flex h-12 w-12 items-center justify-center rounded-full bg-primary">
            <span className="text-xl font-bold text-primary-foreground">
              $
            </span>
          </div>
          <div>
            <p className="text-2xl font-bold">${availableCredits.toFixed(2)}</p>
            <p className="text-sm text-muted-foreground">Available credits</p>
          </div>
        </div>
      </Card>

      {/* Payment Method Section */}
      <Card className="p-6">
        <div className="flex items-center justify-between">
          <h3 className="text-lg font-medium">Payment Method</h3>
          <Button 
            variant="outline"
            onClick={handleCustomerPortal}
          >
            Manage
          </Button>
        </div>
        <div className="mt-4 flex items-center gap-3">
          <CreditCard className="h-6 w-6" />
          <div>
            <p className="font-medium">
              {subscription ? "Payment method on file" : "No payment method"}
            </p>
            <p className="text-sm text-muted-foreground">
              Manage your payment method in Stripe Portal
            </p>
          </div>
        </div>
      </Card>

      {/* Recent Invoices Section */}
      <Card className="p-6">
        <div className="flex items-center justify-between">
          <h3 className="text-lg font-medium">Recent Invoice</h3>
          <Button 
            variant="link"
            onClick={handleCustomerPortal}
          >
            View All
          </Button>
        </div>
        <div className="mt-4 space-y-4">
          {subscriptionHistory?.subscriptions.map((sub) => (
            <div
              key={sub.id}
              className="flex items-center justify-between border-b pb-4 last:border-0"
            >
              <div className="flex items-center gap-3">
                <div className="text-sm">
                  <p className="font-medium">
                    {new Date(sub.currentPeriodStart).toLocaleDateString()}
                  </p>
                  <p className="text-muted-foreground">
                    {sub.plan.name}
                  </p>
                  <p className="text-xs text-muted-foreground capitalize">
                    {sub.status}
                  </p>
                </div>
              </div>
              <Button 
                variant="ghost" 
                size="sm"
                onClick={handleCustomerPortal}
              >
                View Details
              </Button>
            </div>
          ))}
        </div>
      </Card>
    </div>
  );
};
