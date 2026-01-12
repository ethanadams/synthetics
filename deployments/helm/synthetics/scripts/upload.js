import storj from 'k6/x/storj';
import { check } from 'k6';
import { Counter, Trend, Rate } from 'k6/metrics';
import { open } from 'k6';

// Custom metrics for upload operations
const uploadDuration = new Trend('storj_upload_duration_ms');
const uploadSuccess = new Rate('storj_upload_success');
const uploadBytes = new Counter('storj_upload_bytes_total');

export const options = {
    vus: 1,
    iterations: 1,
    thresholds: {
        'storj_upload_success': ['rate>0.95'],
        'storj_upload_duration_ms': ['p(95)<10000'], // 95th percentile < 10s
    },
};

export default function () {
    const accessGrant = __ENV.STORJ_ACCESS_GRANT;
    const bucketName = __ENV.STORJ_BUCKET || 'synthetics-test';
    const fileSize = parseInt(__ENV.FILE_SIZE || '1048576'); // Default 1MB
    const filePrefix = __ENV.FILE_PREFIX || 'synthetics-upload';
    const sharedFile = __ENV.SHARED_FILE; // For test groups (includes ULID)
    const testName = __ENV.TEST_NAME; // Test name
    const testULID = __ENV.TEST_ULID; // ULID for this test run
    const ttlSeconds = parseInt(__ENV.TTL_SECONDS || '0'); // TTL in seconds (0 = no expiration)

    if (!accessGrant) {
        console.error('STORJ_ACCESS_GRANT environment variable is required');
        return;
    }

    // Create Storj client
    const client = storj.newClient(accessGrant);

    try {
        // Get or create test data (cached on filesystem)
        const testData = getOrCreateTestData(testName || 'test-data', fileSize);

        // Use shared filename if in a test group, otherwise generate unique name
        const testKey = sharedFile || `${filePrefix}-${Date.now()}.bin`;

        if (testName && testULID) {
            console.log(`[Test: ${testName}] Using ULID-based filename: ${testKey}`);
            console.log(`[Test: ${testName}] Test ULID: ${testULID}`);
        }

        if (ttlSeconds > 0) {
            console.log(`Uploading ${formatBytes(fileSize)} to ${bucketName}/${testKey} (TTL: ${formatDuration(ttlSeconds)})`);
        } else {
            console.log(`Uploading ${formatBytes(fileSize)} to ${bucketName}/${testKey}`);
        }

        // Upload test
        const uploadStart = Date.now();
        let uploadErr = null;
        try {
            client.upload(bucketName, testKey, testData, ttlSeconds);
        } catch (err) {
            uploadErr = err;
            console.error('Upload failed:', err);
        }
        const uploadEnd = Date.now();
        const uploadDurationMs = uploadEnd - uploadStart;

        uploadDuration.add(uploadDurationMs);
        uploadSuccess.add(uploadErr === null);
        if (uploadErr === null) {
            uploadBytes.add(fileSize);
            console.log(`Upload completed in ${uploadDurationMs}ms (${formatBytes(fileSize)})`);
            console.log(`File: ${testKey}`);
        }

        check(uploadErr, {
            'upload succeeded': (err) => err === null,
        });

    } finally {
        // Always close the client
        try {
            client.close();
        } catch (err) {
            console.warn('Failed to close client:', err);
        }
    }
}

// Get or create test data
// Note: k6's open() has limitations with absolute paths, so we generate in memory
function getOrCreateTestData(testName, size) {
    // Generate test data in memory
    // (Pre-generated files exist on disk from Go startup for other uses)
    return generateTestData(size);
}

// Generate random test data of specified size
function generateTestData(size) {
    const data = new Uint8Array(size);
    for (let i = 0; i < size; i++) {
        data[i] = Math.floor(Math.random() * 256);
    }
    return data;
}

// Format bytes for human-readable output
function formatBytes(bytes) {
    if (bytes < 1024) return bytes + ' B';
    if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(2) + ' KB';
    if (bytes < 1024 * 1024 * 1024) return (bytes / (1024 * 1024)).toFixed(2) + ' MB';
    return (bytes / (1024 * 1024 * 1024)).toFixed(2) + ' GB';
}

// Format duration in seconds for human-readable output
function formatDuration(seconds) {
    if (seconds < 60) return seconds + 's';
    if (seconds < 3600) return Math.floor(seconds / 60) + 'm';
    if (seconds < 86400) return Math.floor(seconds / 3600) + 'h';
    return Math.floor(seconds / 86400) + 'd';
}
