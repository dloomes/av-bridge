import DeviceDetail from './DeviceDetail';

export const metadata = {
  title: 'Device | AV Bridge',
};

export default function Page({ params }: { params: { id: string } }) {
  return <DeviceDetail id={params.id} />;
}
