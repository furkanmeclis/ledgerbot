import { getEnvironments } from '@/lib/environments';

export const dynamic = 'force-dynamic';

export function GET() {
  return Response.json(getEnvironments(), { headers: { 'Cache-Control': 'private, max-age=60' } });
}
