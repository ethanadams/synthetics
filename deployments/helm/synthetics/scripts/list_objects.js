import storj from 'k6/x/storj';
import { check } from 'k6';
import { Trend, Rate } from 'k6/metrics';

// Custom metrics for list operations
const listDuration = new Trend('storj_list_duration_ms');
const listSuccess = new Rate('storj_list_success');
const objectCount = new Trend('storj_object_count');

export const options = {
    vus: 1,
    iterations: 1,
    thresholds: {
        'storj_list_success': ['rate>0.95'],
        'storj_list_duration_ms': ['p(95)<3000'], // 95th percentile < 3s
    },
};

export default function () {
    const accessGrant = __ENV.STORJ_ACCESS_GRANT;
    const bucketName = __ENV.STORJ_BUCKET || 'synthetics-test';

    if (!accessGrant) {
        console.error('STORJ_ACCESS_GRANT environment variable is required');
        return;
    }

    // Create Storj client
    const client = storj.newClient(accessGrant);

    try {
        console.log(`Listing objects in bucket: ${bucketName}`);

        // List objects test
        const listStart = Date.now();
        let listErr = null;
        let objects = null;
        try {
            objects = client.list(bucketName);
        } catch (err) {
            listErr = err;
            console.error('List failed:', err);
        }
        const listEnd = Date.now();
        const listDurationMs = listEnd - listStart;

        listDuration.add(listDurationMs);
        listSuccess.add(listErr === null);

        check(listErr, {
            'list succeeded': (err) => err === null,
        });

        if (objects !== null) {
            objectCount.add(objects.length);
            console.log(`Found ${objects.length} objects in ${listDurationMs}ms`);

            if (objects.length > 0) {
                console.log(`First few objects:`);
                objects.slice(0, 5).forEach((key) => {
                    console.log(`  - ${key}`);
                });
            }
        }

    } finally {
        // Always close the client
        try {
            client.close();
        } catch (err) {
            console.warn('Failed to close client:', err);
        }
    }
}
