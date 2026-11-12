/**
 * @param {import('fastify').FastifyInstance} app
 */
export const registerHealthRoute = async (app) => {
  app.get('/api/healthz', async () => ({
    ok: true,
    service: 'renderer',
  }));
};
