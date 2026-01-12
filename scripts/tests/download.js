import storj from 'k6/x/storj';
import { check } from 'k6';
import { Counter, Trend, Rate } from 'k6/metrics';

// Custom metrics for download operations
const downloadDuration = new Trend('storj_download_duration_ms');
const downloadSuccess = new Rate('storj_download_success');
const downloadBytes = new Counter('storj_download_bytes_total');

export const options = {
    vus: 1,
    iterations: 1,
    thresholds: {
        'storj_download_success': ['rate>0.95'],
        'storj_download_duration_ms': ['p(95)<5000'], // 95th percentile < 5s
    },
};

export default function () {
    const accessGrant = __ENV.STORJ_ACCESS_GRANT;
    const bucketName = __ENV.STORJ_BUCKET || 'synthetics-test';
    const filePrefix = __ENV.FILE_PREFIX || 'synthetics';
    const specificFile = __ENV.FILE_NAME; // Optional: download specific file
    const sharedFile = __ENV.SHARED_FILE; // For test groups (includes ULID)
    const testGroup = __ENV.TEST_GROUP; // Test group name
    const testULID = __ENV.TEST_ULID; // ULID for this test run

    if (!accessGrant) {
        console.error('STORJ_ACCESS_GRANT environment variable is required');
        return;
    }

    // Create Storj client
    const client = storj.newClient(accessGrant);

    try {
        // Priority: SHARED_FILE > FILE_NAME > find by prefix
        let targetFile = sharedFile || specificFile;

        if (testGroup && testULID) {
            console.log(`[Group: ${testGroup}] Downloading ULID-based file: ${sharedFile}`);
            console.log(`[Group: ${testGroup}] Test ULID: ${testULID}`);
        }

        // If no specific file provided, find a file with the prefix
        if (!targetFile) {
            console.log(`Listing files with prefix: ${filePrefix}`);
            const files = client.list(bucketName);

            if (!files || files.length === 0) {
                console.warn('No files found in bucket. Skipping download test.');
                return;
            }

            // Filter files by prefix
            const matchingFiles = files.filter(f => f.startsWith(filePrefix));

            if (matchingFiles.length === 0) {
                console.warn(`No files found with prefix: ${filePrefix}. Skipping download test.`);
                return;
            }

            // Pick a random file
            targetFile = matchingFiles[Math.floor(Math.random() * matchingFiles.length)];
            console.log(`Found ${matchingFiles.length} matching files, selected: ${targetFile}`);
        }

        console.log(`Downloading ${bucketName}/${targetFile}`);

        // Download test
        const downloadStart = Date.now();
        let downloadErr = null;
        let downloadData = null;
        try {
            downloadData = client.download(bucketName, targetFile);
        } catch (err) {
            downloadErr = err;
            console.error('Download failed:', err);
        }
        const downloadEnd = Date.now();
        const downloadDurationMs = downloadEnd - downloadStart;

        downloadDuration.add(downloadDurationMs);
        downloadSuccess.add(downloadErr === null);
        if (downloadErr === null && downloadData !== null) {
            downloadBytes.add(downloadData.length);
            console.log(`Download completed in ${downloadDurationMs}ms (${formatBytes(downloadData.length)})`);
        }

        check(downloadErr, {
            'download succeeded': (err) => err === null,
        });

        if (downloadData !== null) {
            check(downloadData, {
                'downloaded data is not empty': (data) => data.length > 0,
            });
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

// Format bytes for human-readable output
function formatBytes(bytes) {
    if (bytes < 1024) return bytes + ' B';
    if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(2) + ' KB';
    if (bytes < 1024 * 1024 * 1024) return (bytes / (1024 * 1024)).toFixed(2) + ' MB';
    return (bytes / (1024 * 1024 * 1024)).toFixed(2) + ' GB';
}
